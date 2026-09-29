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
	"log/slog"
	"math/big"
	"mime"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/calmshv-star/ocrypt/backend/internal/rates"
)

const (
	coinGeckoURL          = "https://api.coingecko.com/api/v3/simple/price?ids=%s&vs_currencies=%s&include_last_updated_at=true"
	coinPaprikaURL        = "https://api.coinpaprika.com/v1/tickers/%s?quotes=%s"
	coinMarketCapURL      = "https://pro-api.coinmarketcap.com/public-api/v3/cryptocurrency/quotes/latest?id=%s&convert=%s"
	coinMarketCapKeyedURL = "https://pro-api.coinmarketcap.com/v3/cryptocurrency/quotes/latest?id=%s&convert=%s"
	kazakhstanRatesURL    = "https://nationalbank.kz/rss/rates_all.xml"
	coinPaprikaSpacing    = 300 * time.Millisecond
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
	"usdt-polygon":   {ID: "usdt-polygon", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"usdce-polygon":  {ID: "usdce-polygon", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"bnb-bsc":        {ID: "bnb-bsc", CoinGeckoID: "binancecoin", CoinPaprikaID: "bnb-binance-coin"},
	"usdt-bsc":       {ID: "usdt-bsc", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"usdc-bsc":       {ID: "usdc-bsc", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"usdt-plasma":    {ID: "usdt-plasma", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
	"usdc-aptos":     {ID: "usdc-aptos", CoinGeckoID: "usd-coin", CoinPaprikaID: "usdc-usd-coin"},
	"usdt-aptos":     {ID: "usdt-aptos", CoinGeckoID: "tether", CoinPaprikaID: "usdt-tether"},
}

// CoinMarketCap IDs avoid ticker collisions: TON is now GRAM (11419), while
// a symbol lookup for TON currently resolves to an unrelated stock token.
var coinMarketCapIDs = map[string]string{
	"ethereum": "1027", "usd-coin": "3408", "tether": "825",
	"solana": "5426", "the-open-network": "11419", "tron": "1958",
	"avalanche-2": "5805", "polygon-ecosystem-token": "28321", "binancecoin": "1839",
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
		client:               client,
		coinMarketCapKey:     os.Getenv("COINMARKETCAP_API_KEY"),
		coinMarketCapCache:   make(map[string]map[string]rates.ProviderResult),
		coinMarketCapExpires: make(map[string]time.Time),
		coinGeckoCache:       make(map[string]map[string]rates.ProviderResult),
		coinGeckoExpires:     make(map[string]time.Time),
		coinPaprikaCache:     make(map[string]upstreamQuote),
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
	if !ok || !currencyOK || provider != "coingecko" && provider != "coinpaprika" && provider != "coinmarketcap" || g.fetcher == nil || g.now == nil {
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
			// Asset and provider identifiers are public catalog values; raw bodies,
			// endpoints, credentials, and response evidence are intentionally omitted.
			slog.Warn("rate gateway upstream rejected", "provider", provider, "asset", configured.ID, "currency", currency, "reason", boundedUpstreamReason(err))
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

func boundedUpstreamReason(err error) string {
	if err == nil {
		return "unknown"
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "CoinMarketCap rate is missing"):
		return "coinmarketcap_missing"
	case strings.Contains(message, "invalid CoinMarketCap response"):
		return "coinmarketcap_schema"
	case strings.Contains(message, "invalid CoinPaprika response"):
		return "coinpaprika_schema"
	case strings.Contains(message, "CoinPaprika rate is missing"):
		return "coinpaprika_missing"
	case strings.Contains(message, "invalid CoinGecko response"):
		return "coingecko_schema"
	case strings.Contains(message, "CoinGecko rate is missing"):
		return "coingecko_missing"
	case strings.Contains(message, "invalid response"):
		return "http_response"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, context.Canceled):
		return "canceled"
	default:
		return "unavailable"
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
	client               *http.Client
	coinMarketCapKey     string
	coinMarketCapMu      sync.Mutex
	coinMarketCapCache   map[string]map[string]rates.ProviderResult
	coinMarketCapExpires map[string]time.Time

	coinGeckoMu      sync.Mutex
	coinGeckoCache   map[string]map[string]rates.ProviderResult
	coinGeckoExpires map[string]time.Time

	coinPaprikaMu    sync.Mutex
	coinPaprikaCache map[string]upstreamQuote
	coinPaprikaLast  time.Time

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
	case "coinmarketcap":
		return u.coinMarketCap(ctx, currency, configured)
	default:
		return rates.ProviderResult{}, errors.New("unknown rate provider")
	}
}

func (u *upstream) coinMarketCap(ctx context.Context, currency string, configured asset) (rates.ProviderResult, error) {
	u.coinMarketCapMu.Lock()
	defer u.coinMarketCapMu.Unlock()
	if batch, ok := u.coinMarketCapCache[currency]; ok && u.coinMarketCapExpires[currency].After(time.Now()) {
		if value, found := batch[configured.ID]; found {
			return value, nil
		}
	}
	ids := sortedCoinMarketCapIDs()
	endpoint := coinMarketCapURL
	if u.coinMarketCapKey != "" {
		endpoint = coinMarketCapKeyedURL
	}
	raw, err := u.getTypedWithKey(ctx, fmt.Sprintf(endpoint, strings.Join(ids, ","), currency), "application/json", u.coinMarketCapKey)
	if err != nil {
		return rates.ProviderResult{}, err
	}
	var envelope struct {
		Data []struct {
			ID          int    `json:"id"`
			LastUpdated string `json:"last_updated"`
			Quotes      []struct {
				Symbol      string      `json:"symbol"`
				Price       json.Number `json:"price"`
				LastUpdated string      `json:"last_updated"`
			} `json:"quote"`
		} `json:"data"`
		Status struct {
			Timestamp string          `json:"timestamp"`
			ErrorCode json.RawMessage `json:"error_code"`
		} `json:"status"`
	}
	if json.Unmarshal(raw, &envelope) != nil || (string(envelope.Status.ErrorCode) != `"0"` && string(envelope.Status.ErrorCode) != "0") {
		return rates.ProviderResult{}, errors.New("invalid CoinMarketCap response")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, envelope.Status.Timestamp)
	if err != nil || time.Since(observedAt) > 5*time.Minute || observedAt.After(time.Now().Add(time.Minute)) {
		return rates.ProviderResult{}, errors.New("invalid CoinMarketCap response")
	}
	quotes := make(map[string]upstreamQuote, len(envelope.Data))
	for _, item := range envelope.Data {
		id := fmt.Sprint(item.ID)
		if len(item.Quotes) != 1 || item.Quotes[0].Symbol != currency || quotes[id].Price != nil {
			return rates.ProviderResult{}, errors.New("invalid CoinMarketCap response")
		}
		price, ok := new(big.Rat).SetString(item.Quotes[0].Price.String())
		if !ok || price.Sign() <= 0 {
			return rates.ProviderResult{}, errors.New("invalid CoinMarketCap response")
		}
		assetUpdated, assetErr := time.Parse(time.RFC3339Nano, item.LastUpdated)
		quoteUpdated, quoteErr := time.Parse(time.RFC3339Nano, item.Quotes[0].LastUpdated)
		if assetErr != nil || quoteErr != nil || assetUpdated.After(time.Now().Add(10*time.Second)) || quoteUpdated.After(time.Now().Add(10*time.Second)) {
			return rates.ProviderResult{}, errors.New("invalid CoinMarketCap response")
		}
		if quoteUpdated.After(assetUpdated) {
			quoteUpdated = assetUpdated
		}
		quotes[id] = upstreamQuote{Price: price, ObservedAt: quoteUpdated.UTC()}
	}
	batch := make(map[string]rates.ProviderResult, len(assets))
	for _, candidate := range assets {
		id := coinMarketCapIDs[candidate.CoinGeckoID]
		quote, found := quotes[id]
		if !found {
			continue
		}
		value, normalizeErr := normalizedRational(candidate.ID, currency, "coinmarketcap", quote.Price, quote.ObservedAt, raw)
		if normalizeErr != nil {
			return rates.ProviderResult{}, normalizeErr
		}
		batch[candidate.ID] = value
	}
	u.coinMarketCapCache[currency] = batch
	u.coinMarketCapExpires[currency] = time.Now().Add(time.Minute)
	value, found := batch[configured.ID]
	if !found {
		return rates.ProviderResult{}, errors.New("CoinMarketCap rate is missing")
	}
	return value, nil
}

func sortedCoinMarketCapIDs() []string {
	ids := make([]string, 0, len(coinMarketCapIDs))
	for _, id := range coinMarketCapIDs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
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
		if wait := time.Until(u.coinPaprikaLast.Add(coinPaprikaSpacing)); wait > 0 {
			timer := time.NewTimer(wait)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return rates.ProviderResult{}, ctx.Err()
			case <-timer.C:
			}
		}
		u.coinPaprikaLast = time.Now()
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
	return u.getTypedWithKey(ctx, endpoint, expectedType, "")
}

func (u *upstream) getTypedWithKey(ctx context.Context, endpoint, expectedType, apiKey string) ([]byte, error) {
	if u == nil || u.client == nil {
		return nil, errors.New("rate HTTP client is missing")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", expectedType)
	request.Header.Set("User-Agent", "ocrypt-rate-gateway/1")
	if apiKey != "" {
		request.Header.Set("X-CMC_PRO_API_KEY", apiKey)
	}
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
