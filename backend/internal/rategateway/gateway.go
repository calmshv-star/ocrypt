package rategateway

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"math/big"
	"mime"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/rates"
)

const (
	coinGeckoURL       = "https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=%s&include_last_updated_at=true"
	coinPaprikaURL     = "https://api.coinpaprika.com/v1/tickers/%s?quotes=%s"
	kazakhstanRatesURL = "https://nationalbank.kz/rss/rates_all.xml"
)

// defaultFiatCurrencies is the closed, ready-to-use invoice-currency catalog.
// Keep deploy/standalone/bootstrap-rates.sql and RATE_TARGETS_JSON in sync.
var defaultFiatCurrencies = []string{"RUB", "USD", "EUR", "KZT", "INR", "CNY"}

var supportedFiat = func() map[string]struct{} {
	result := make(map[string]struct{}, len(defaultFiatCurrencies))
	for _, currency := range defaultFiatCurrencies {
		result[currency] = struct{}{}
	}
	return result
}()

type asset struct {
	ID            string
	CoinGeckoID   string
	CoinPaprikaID string
}

var assets = map[string]asset{
	"eth-ethereum":   {ID: "eth-ethereum", CoinGeckoID: "ethereum", CoinPaprikaID: "eth-ethereum"},
	"usdc-ethereum":  {ID: "usdc-ethereum", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"usdt-ethereum":  {ID: "usdt-ethereum", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"sol-solana":     {ID: "sol-solana", CoinGeckoID: "solana", CoinPaprikaID: "sol-solana"},
	"usdc-solana":    {ID: "usdc-solana", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"usdt-solana":    {ID: "usdt-solana", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"ton-ton":        {ID: "ton-ton", CoinGeckoID: "the-open-network", CoinPaprikaID: "toncoin-the-open-network"},
	"usdt-ton":       {ID: "usdt-ton", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"trx-tron":       {ID: "trx-tron", CoinGeckoID: "tron", CoinPaprikaID: "trx-tron"},
	"usdt-tron":      {ID: "usdt-tron", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"eth-base":       {ID: "eth-base", CoinGeckoID: "ethereum", CoinPaprikaID: "eth-ethereum"},
	"usdc-base":      {ID: "usdc-base", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"eth-arbitrum":   {ID: "eth-arbitrum", CoinGeckoID: "ethereum", CoinPaprikaID: "eth-ethereum"},
	"usdc-arbitrum":  {ID: "usdc-arbitrum", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"eth-optimism":   {ID: "eth-optimism", CoinGeckoID: "ethereum", CoinPaprikaID: "eth-ethereum"},
	"usdc-optimism":  {ID: "usdc-optimism", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"avax-avalanche": {ID: "avax-avalanche", CoinGeckoID: "avalanche-2", CoinPaprikaID: "avax-avalanche"},
	"usdc-avalanche": {ID: "usdc-avalanche", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"pol-polygon":    {ID: "pol-polygon", CoinGeckoID: "polygon-ecosystem-token", CoinPaprikaID: "pol-polygon-ecosystem-token"},
	"usdc-polygon":   {ID: "usdc-polygon", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"bnb-bsc":        {ID: "bnb-bsc", CoinGeckoID: "binancecoin", CoinPaprikaID: "bnb-binance-coin"},
}

type Fetcher interface {
	Fetch(context.Context, string, string, asset) (rates.ProviderResult, error)
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
		Proxy:       nil,
		DialContext: (&net.Dialer{Timeout: 4 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		// Public market-data sources include an official central-bank endpoint
		// that currently negotiates TLS 1.2. Older protocol versions stay blocked.
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 4 * time.Second,
		IdleConnTimeout:     30 * time.Second,
		ForceAttemptHTTP2:   true,
	}
	client := &http.Client{Transport: transport, Timeout: 6 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("rate source redirects are disabled")
	}}
	return NewWithFetcher(&upstream{
		client:           client,
		coinGeckoCache:   make(map[string]map[string]rates.ProviderResult),
		coinGeckoExpires: make(map[string]time.Time),
		coinPaprikaCache: make(map[string]upstreamQuote),
	}, func() time.Time { return time.Now().UTC() })
}

func NewWithFetcher(fetcher Fetcher, now func() time.Time) *Gateway {
	return &Gateway{fetcher: fetcher, now: now, cache: make(map[string]cached)}
}

func (g *Gateway) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/public/rates/{provider}/{asset}/{currency}", g.get)
	// The original endpoint remains a RUB alias so existing admitted snapshots
	// keep working during a rolling upgrade.
	mux.HandleFunc("GET /v1/public/rates/{provider}/{asset}", g.get)
	return mux
}

func (g *Gateway) get(response http.ResponseWriter, request *http.Request) {
	provider := request.PathValue("provider")
	configured, ok := assets[request.PathValue("asset")]
	currency := request.PathValue("currency")
	if currency == "" {
		currency = "RUB"
	}
	_, currencyOK := supportedFiat[currency]
	if !ok || !currencyOK || provider != "coingecko" && provider != "coinpaprika" || g.fetcher == nil || g.now == nil {
		http.NotFound(response, request)
		return
	}
	key := provider + "\x00" + configured.ID + "\x00" + currency
	now := g.now().UTC()
	g.mu.Lock()
	entry, fresh := g.cache[key]
	fresh = fresh && entry.ExpiresAt.After(now)
	g.mu.Unlock()
	if !fresh {
		value, err := g.fetcher.Fetch(request.Context(), provider, currency, configured)
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

type upstreamQuote struct {
	Price      *big.Rat
	ObservedAt time.Time
	Raw        []byte
	ExpiresAt  time.Time
}

type upstream struct {
	client *http.Client

	coinGeckoMu      sync.Mutex
	coinGeckoCache   map[string]map[string]rates.ProviderResult
	coinGeckoExpires map[string]time.Time

	coinPaprikaMu    sync.Mutex
	coinPaprikaCache map[string]upstreamQuote

	kazakhstanMu     sync.Mutex
	kazakhstanFactor upstreamQuote
}

func (u *upstream) Fetch(ctx context.Context, provider, currency string, configured asset) (rates.ProviderResult, error) {
	if _, ok := supportedFiat[currency]; !ok {
		return rates.ProviderResult{}, errors.New("unsupported invoice currency")
	}
	switch provider {
	case "coingecko":
		return u.coinGecko(ctx, currency, configured)
	case "coinpaprika":
		return u.coinPaprika(ctx, currency, configured)
	default:
		return rates.ProviderResult{}, errors.New("unknown rate provider")
	}
}

func (u *upstream) coinGecko(ctx context.Context, currency string, configured asset) (rates.ProviderResult, error) {
	u.coinGeckoMu.Lock()
	defer u.coinGeckoMu.Unlock()
	if batch, ok := u.coinGeckoCache[currency]; ok && u.coinGeckoExpires[currency].After(time.Now()) {
		if value, found := batch[configured.ID]; found {
			return value, nil
		}
	}
	ids := sortedCoinGeckoIDs()
	raw, err := u.get(ctx, fmt.Sprintf(coinGeckoURL, strings.Join(ids, ","), "rub,usd,eur,inr,cny"))
	if err != nil {
		return rates.ProviderResult{}, err
	}
	var envelope map[string]struct {
		USD           json.Number `json:"usd,omitempty"`
		RUB           json.Number `json:"rub,omitempty"`
		EUR           json.Number `json:"eur,omitempty"`
		INR           json.Number `json:"inr,omitempty"`
		CNY           json.Number `json:"cny,omitempty"`
		LastUpdatedAt int64       `json:"last_updated_at"`
	}
	if strictDecode(raw, &envelope) != nil {
		return rates.ProviderResult{}, errors.New("invalid CoinGecko response")
	}
	now := time.Now()
	for _, targetCurrency := range defaultFiatCurrencies {
		upstreamCurrency := targetCurrency
		factor := big.NewRat(1, 1)
		evidence := raw
		if targetCurrency == "KZT" {
			upstreamCurrency = "USD"
			factor, evidence, err = u.kazakhstanCross(ctx, raw)
			if err != nil {
				if currency == "KZT" {
					return rates.ProviderResult{}, err
				}
				continue
			}
		}
		batch := make(map[string]rates.ProviderResult, len(assets))
		for _, candidate := range assets {
			quote, ok := envelope[candidate.CoinGeckoID]
			if !ok || quote.LastUpdatedAt <= 0 {
				continue
			}
			decimal := coinGeckoDecimal(quote, upstreamCurrency)
			price, priceOK := new(big.Rat).SetString(decimal)
			if !priceOK {
				continue
			}
			price.Mul(price, factor)
			value, normalizeErr := normalizedRational(candidate.ID, targetCurrency, "coingecko", price, time.Unix(quote.LastUpdatedAt, 0).UTC(), evidence)
			if normalizeErr == nil {
				batch[candidate.ID] = value
			}
		}
		u.coinGeckoCache[targetCurrency] = batch
		u.coinGeckoExpires[targetCurrency] = now.Add(30 * time.Second)
	}
	value, ok := u.coinGeckoCache[currency][configured.ID]
	if !ok {
		return rates.ProviderResult{}, errors.New("CoinGecko rate is missing")
	}
	return value, nil
}

func coinGeckoDecimal(quote struct {
	USD           json.Number `json:"usd,omitempty"`
	RUB           json.Number `json:"rub,omitempty"`
	EUR           json.Number `json:"eur,omitempty"`
	INR           json.Number `json:"inr,omitempty"`
	CNY           json.Number `json:"cny,omitempty"`
	LastUpdatedAt int64       `json:"last_updated_at"`
}, currency string) string {
	switch currency {
	case "USD":
		return quote.USD.String()
	case "RUB":
		return quote.RUB.String()
	case "EUR":
		return quote.EUR.String()
	case "INR":
		return quote.INR.String()
	case "CNY":
		return quote.CNY.String()
	default:
		return ""
	}
}

func sortedCoinGeckoIDs() []string {
	unique := make(map[string]struct{}, len(assets))
	for _, configured := range assets {
		unique[configured.CoinGeckoID] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for id := range unique {
		result = append(result, id)
	}
	sort.Strings(result)
	return result
}

func (u *upstream) coinPaprika(ctx context.Context, currency string, configured asset) (rates.ProviderResult, error) {
	u.coinPaprikaMu.Lock()
	defer u.coinPaprikaMu.Unlock()
	upstreamCurrency := currency
	if currency == "KZT" {
		upstreamCurrency = "USD"
	}
	// Several chain-specific assets intentionally share one market quote (for
	// example native USDC on Base and Arbitrum). Cache the raw upstream quote by
	// the provider's identity so aliases cannot fan out into duplicate public API
	// calls and hit the provider's free-tier rate limit.
	cacheKey := configured.CoinPaprikaID + "\x00" + upstreamCurrency
	quote, fresh := u.coinPaprikaCache[cacheKey]
	fresh = fresh && quote.ExpiresAt.After(time.Now())
	if !fresh {
		group := []string{"RUB", "USD", "EUR"}
		if upstreamCurrency == "INR" || upstreamCurrency == "CNY" {
			group = []string{"INR", "CNY"}
		}
		raw, err := u.get(ctx, fmt.Sprintf(coinPaprikaURL, configured.CoinPaprikaID, strings.Join(group, ",")))
		if err != nil {
			return rates.ProviderResult{}, err
		}
		parsed, parseErr := parseCoinPaprika(raw, configured, group)
		if parseErr != nil {
			return rates.ProviderResult{}, parseErr
		}
		for parsedCurrency, parsedQuote := range parsed {
			parsedQuote.ExpiresAt = time.Now().Add(2 * time.Minute)
			u.coinPaprikaCache[configured.CoinPaprikaID+"\x00"+parsedCurrency] = parsedQuote
		}
		quote, fresh = u.coinPaprikaCache[cacheKey]
		if !fresh {
			return rates.ProviderResult{}, errors.New("CoinPaprika rate is missing")
		}
	}
	price := new(big.Rat).Set(quote.Price)
	combined := quote.Raw
	if currency == "KZT" {
		factor, joined, err := u.kazakhstanCross(ctx, quote.Raw)
		if err != nil {
			return rates.ProviderResult{}, err
		}
		price.Mul(price, factor)
		combined = joined
	}
	return normalizedRational(configured.ID, currency, "coinpaprika", price, quote.ObservedAt, combined)
}

func parseCoinPaprika(raw []byte, configured asset, currencies []string) (map[string]upstreamQuote, error) {
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
		return nil, errors.New("invalid CoinPaprika response")
	}
	observedAt, timeErr := time.Parse(time.RFC3339, envelope.LastUpdated)
	if envelope.ID != configured.CoinPaprikaID || timeErr != nil || len(envelope.Quotes) != len(currencies) {
		return nil, errors.New("CoinPaprika rate is missing")
	}
	result := make(map[string]upstreamQuote, len(currencies))
	for _, currency := range currencies {
		value, found := envelope.Quotes[currency]
		price, priceOK := new(big.Rat).SetString(value.Price.String())
		if !found || !priceOK || price.Sign() <= 0 {
			return nil, errors.New("CoinPaprika rate is missing")
		}
		result[currency] = upstreamQuote{Price: price, ObservedAt: observedAt.UTC(), Raw: raw}
	}
	return result, nil
}

func (u *upstream) kazakhstanCross(ctx context.Context, cryptoRaw []byte) (*big.Rat, []byte, error) {
	u.kazakhstanMu.Lock()
	defer u.kazakhstanMu.Unlock()
	if u.kazakhstanFactor.Price == nil || !u.kazakhstanFactor.ExpiresAt.After(time.Now()) {
		raw, err := u.getXML(ctx, kazakhstanRatesURL)
		if err != nil {
			return nil, nil, err
		}
		factor, observedAt, parseErr := parseKazakhstanUSD(raw, time.Now().UTC())
		if parseErr != nil {
			return nil, nil, parseErr
		}
		u.kazakhstanFactor = upstreamQuote{
			Price: factor, ObservedAt: observedAt, Raw: raw,
			ExpiresAt: time.Now().Add(6 * time.Hour),
		}
	}
	return new(big.Rat).Set(u.kazakhstanFactor.Price), joinEvidence(cryptoRaw, u.kazakhstanFactor.Raw), nil
}

func parseKazakhstanUSD(raw []byte, now time.Time) (*big.Rat, time.Time, error) {
	var feed struct {
		Channel struct {
			Items []struct {
				Title       string `xml:"title"`
				Published   string `xml:"pubDate"`
				Description string `xml:"description"`
				Quantity    string `xml:"quant"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(raw, &feed); err != nil {
		return nil, time.Time{}, errors.New("invalid National Bank of Kazakhstan response")
	}
	for _, item := range feed.Channel.Items {
		if item.Title != "USD" {
			continue
		}
		price, priceOK := new(big.Rat).SetString(item.Description)
		quantity, quantityOK := new(big.Rat).SetString(item.Quantity)
		observedAt, timeErr := time.ParseInLocation("02.01.2006", item.Published, time.FixedZone("Asia/Almaty", 5*60*60))
		if !priceOK || !quantityOK || price.Sign() <= 0 || quantity.Sign() <= 0 || timeErr != nil {
			break
		}
		age := now.Sub(observedAt.UTC())
		if age < -12*time.Hour || age > 72*time.Hour {
			return nil, time.Time{}, errors.New("National Bank of Kazakhstan rate is stale")
		}
		return price.Quo(price, quantity), observedAt.UTC(), nil
	}
	return nil, time.Time{}, errors.New("National Bank of Kazakhstan USD/KZT rate is missing")
}

func joinEvidence(parts ...[]byte) []byte {
	result := make([]byte, 0)
	for _, part := range parts {
		result = append(result, fmt.Sprintf("%d:", len(part))...)
		result = append(result, part...)
	}
	return result
}

func (u *upstream) get(ctx context.Context, endpoint string) ([]byte, error) {
	return u.getTyped(ctx, endpoint, "application/json")
}

func (u *upstream) getXML(ctx context.Context, endpoint string) ([]byte, error) {
	return u.getTyped(ctx, endpoint, "application/xml")
}

func (u *upstream) getTyped(ctx context.Context, endpoint, expectedType string) ([]byte, error) {
	if u == nil || u.client == nil {
		return nil, errors.New("rate HTTP client is missing")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", expectedType)
	request.Header.Set("User-Agent", "ocrypt-rate-gateway/1")
	result, err := u.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	contentTypes := result.Header.Values("Content-Type")
	mediaType, _, typeErr := mime.ParseMediaType(result.Header.Get("Content-Type"))
	validType := mediaType == expectedType || expectedType == "application/xml" && (mediaType == "text/xml" || mediaType == "application/rss+xml")
	if result.StatusCode != http.StatusOK || len(contentTypes) != 1 || typeErr != nil || !validType {
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
	if !ok {
		return rates.ProviderResult{}, errors.New("rate provider returned an invalid price")
	}
	return normalizedRational(base, quote, provider, value, observedAt, raw)
}

func normalizedRational(base, quote, provider string, value *big.Rat, observedAt time.Time, raw []byte) (rates.ProviderResult, error) {
	if value == nil || value.Sign() <= 0 || observedAt.IsZero() {
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
