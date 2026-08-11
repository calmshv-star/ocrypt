package rategateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/rates"
)

const (
	coinGeckoURL   = "https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=rub&include_last_updated_at=true"
	coinPaprikaURL = "https://api.coinpaprika.com/v1/tickers/%s?quotes=RUB"
)

type asset struct {
	ID            string
	CoinGeckoID   string
	CoinPaprikaID string
}

var assets = map[string]asset{
	"eth-ethereum": {ID: "eth-ethereum", CoinGeckoID: "ethereum", CoinPaprikaID: "eth-ethereum"},
	"sol-solana":   {ID: "sol-solana", CoinGeckoID: "solana", CoinPaprikaID: "sol-solana"},
	"ton-ton":      {ID: "ton-ton", CoinGeckoID: "the-open-network", CoinPaprikaID: "toncoin-the-open-network"},
	"trx-tron":     {ID: "trx-tron", CoinGeckoID: "tron", CoinPaprikaID: "trx-tron"},
	"usdt-tron":    {ID: "usdt-tron", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
}

type Fetcher interface {
	Fetch(context.Context, string, asset) (rates.ProviderResult, error)
}

type cached struct {
	Value     rates.ProviderResult
	ExpiresAt time.Time
}

type Gateway struct {
	fetcher Fetcher
	now     func() time.Time
	mu      sync.Mutex
	cache   map[string]cached
}

func New() *Gateway {
	transport := &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS13},
		TLSHandshakeTimeout: 4 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("rate source redirects are disabled")
	}}
	return NewWithFetcher(&upstream{client: client, coinGeckoCache: make(map[string]rates.ProviderResult)}, func() time.Time { return time.Now().UTC() })
}

func NewWithFetcher(fetcher Fetcher, now func() time.Time) *Gateway {
	return &Gateway{fetcher: fetcher, now: now, cache: make(map[string]cached)}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/public/rates/{provider}/{asset}", g.get)
	return mux
}

func (g *Gateway) get(response http.ResponseWriter, request *http.Request) {
	provider := request.PathValue("provider")
	configured, ok := assets[request.PathValue("asset")]
	if !ok || provider != "coingecko" && provider != "coinpaprika" || g.fetcher == nil || g.now == nil {
		http.NotFound(response, request)
		return
	}
	key := provider + "\x00" + configured.ID
	now := g.now().UTC()
	g.mu.Lock()
	entry, fresh := g.cache[key]
	fresh = fresh && entry.ExpiresAt.After(now)
	g.mu.Unlock()
	if !fresh {
		value, err := g.fetcher.Fetch(request.Context(), provider, configured)
		if err != nil {
			writeError(response, http.StatusBadGateway)
			return
		}
		entry = cached{Value: value, ExpiresAt: now.Add(15 * time.Second)}
		g.mu.Lock()
		g.cache[key] = entry
		g.mu.Unlock()
	}
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "public, max-age=10")
	response.Header().Set("X-Content-Type-Options", "nosniff")
	if err := json.NewEncoder(response).Encode(entry.Value); err != nil {
		return
	}
}

func writeError(response http.ResponseWriter, status int) {
	response.Header().Set("Content-Type", "application/json")
	response.Header().Set("Cache-Control", "no-store")
	response.WriteHeader(status)
	_, _ = io.WriteString(response, `{"error":{"code":"rate_source_unavailable"}}`+"\n")
}

type upstream struct {
	client           *http.Client
	mu               sync.Mutex
	coinGeckoCache   map[string]rates.ProviderResult
	coinGeckoExpires time.Time
}

func (u *upstream) Fetch(ctx context.Context, provider string, configured asset) (rates.ProviderResult, error) {
	switch provider {
	case "coingecko":
		return u.coinGecko(ctx, configured)
	case "coinpaprika":
		return u.coinPaprika(ctx, configured)
	default:
		return rates.ProviderResult{}, errors.New("unknown rate provider")
	}
}

func (u *upstream) coinGecko(ctx context.Context, configured asset) (rates.ProviderResult, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if value, ok := u.coinGeckoCache[configured.ID]; ok && u.coinGeckoExpires.After(time.Now()) {
		return value, nil
	}
	raw, err := u.get(ctx, fmt.Sprintf(coinGeckoURL, "ethereum,solana,the-open-network,tron,tether"))
	if err != nil {
		return rates.ProviderResult{}, err
	}
	var envelope map[string]struct {
		RUB           json.Number `json:"rub"`
		LastUpdatedAt int64       `json:"last_updated_at"`
	}
	if strictDecode(raw, &envelope) != nil {
		return rates.ProviderResult{}, errors.New("invalid CoinGecko response")
	}
	batch := make(map[string]rates.ProviderResult, len(assets))
	for _, candidate := range assets {
		quote, ok := envelope[candidate.CoinGeckoID]
		if !ok || quote.LastUpdatedAt <= 0 {
			continue
		}
		value, normalizeErr := normalized(candidate.ID, "RUB", "coingecko", quote.RUB.String(), time.Unix(quote.LastUpdatedAt, 0).UTC(), raw)
		if normalizeErr == nil {
			batch[candidate.ID] = value
		}
	}
	value, ok := batch[configured.ID]
	if !ok {
		return rates.ProviderResult{}, errors.New("CoinGecko rate is missing")
	}
	u.coinGeckoCache = batch
	u.coinGeckoExpires = time.Now().Add(30 * time.Second)
	return value, nil
}

func (u *upstream) coinPaprika(ctx context.Context, configured asset) (rates.ProviderResult, error) {
	raw, err := u.get(ctx, fmt.Sprintf(coinPaprikaURL, configured.CoinPaprikaID))
	if err != nil {
		return rates.ProviderResult{}, err
	}
	var envelope struct {
		ID          string          `json:"id"`
		Name        json.RawMessage `json:"name"`
		Symbol      json.RawMessage `json:"symbol"`
		Rank        json.RawMessage `json:"rank"`
		TotalSupply json.RawMessage `json:"total_supply"`
		MaxSupply   json.RawMessage `json:"max_supply"`
		BetaValue   json.RawMessage `json:"beta_value"`
		FirstDataAt json.RawMessage `json:"first_data_at"`
		LastUpdated string          `json:"last_updated"`
		Quotes      map[string]struct {
			Price               json.Number     `json:"price"`
			Volume24H           json.RawMessage `json:"volume_24h"`
			VolumeChange24H     json.RawMessage `json:"volume_24h_change_24h"`
			MarketCap           json.RawMessage `json:"market_cap"`
			MarketCapChange24H  json.RawMessage `json:"market_cap_change_24h"`
			Change15M           json.RawMessage `json:"percent_change_15m"`
			Change30M           json.RawMessage `json:"percent_change_30m"`
			Change1H            json.RawMessage `json:"percent_change_1h"`
			Change6H            json.RawMessage `json:"percent_change_6h"`
			Change12H           json.RawMessage `json:"percent_change_12h"`
			Change24H           json.RawMessage `json:"percent_change_24h"`
			Change7D            json.RawMessage `json:"percent_change_7d"`
			Change30D           json.RawMessage `json:"percent_change_30d"`
			Change1Y            json.RawMessage `json:"percent_change_1y"`
			ATHPrice            json.RawMessage `json:"ath_price"`
			ATHDate             json.RawMessage `json:"ath_date"`
			PercentFromPriceATH json.RawMessage `json:"percent_from_price_ath"`
		} `json:"quotes"`
	}
	if strictDecode(raw, &envelope) != nil {
		return rates.ProviderResult{}, errors.New("invalid CoinPaprika response")
	}
	quote, found := envelope.Quotes["RUB"]
	observedAt, timeErr := time.Parse(time.RFC3339, envelope.LastUpdated)
	if envelope.ID != configured.CoinPaprikaID || !found || timeErr != nil {
		return rates.ProviderResult{}, errors.New("CoinPaprika rate is missing")
	}
	return normalized(configured.ID, "RUB", "coinpaprika", quote.Price.String(), observedAt.UTC(), raw)
}

func (u *upstream) get(ctx context.Context, endpoint string) ([]byte, error) {
	if u == nil || u.client == nil {
		return nil, errors.New("rate HTTP client is missing")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "ocrypt-rate-gateway/1")
	result, err := u.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	contentTypes := result.Header.Values("Content-Type")
	mediaType, _, typeErr := mime.ParseMediaType(result.Header.Get("Content-Type"))
	if result.StatusCode != http.StatusOK || len(contentTypes) != 1 || typeErr != nil || mediaType != "application/json" {
		return nil, errors.New("rate provider returned an invalid response")
	}
	raw, err := io.ReadAll(io.LimitReader(result.Body, (256<<10)+1))
	if err != nil || len(raw) > 256<<10 {
		return nil, errors.New("rate provider response exceeded the limit")
	}
	return raw, nil
}

func normalized(base, quote, provider, decimal string, observedAt time.Time, raw []byte) (rates.ProviderResult, error) {
	value, ok := new(big.Rat).SetString(decimal)
	if !ok || value.Sign() <= 0 || observedAt.IsZero() {
		return rates.ProviderResult{}, errors.New("rate provider returned an invalid price")
	}
	digest := sha256.Sum256(raw)
	return rates.ProviderResult{
		BaseAsset: base, QuoteAsset: quote,
		PriceNumerator: value.Num().String(), PriceDenominator: value.Denom().String(),
		ObservedAt: observedAt.UTC(), ProviderObservationID: provider + ":" + fmt.Sprint(observedAt.Unix()) + ":" + hex.EncodeToString(digest[:8]),
	}, nil
}

func strictDecode(raw []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}
