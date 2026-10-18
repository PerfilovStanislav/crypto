package exchange

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"trader/config"
	"trader/db"
)

const defaultBaseURL = "https://api.bybit.com"

const recvWindow = "5000"

type Client struct {
	apiKey     string
	apiSecret  string
	baseURL    string
	httpClient *http.Client
}

func NewClient(cfg config.BybitConfig) *Client {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		apiKey:     cfg.APIKey,
		apiSecret:  cfg.APISecret,
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *Client) sign(timestamp, payload string) string {
	val := timestamp + c.apiKey + recvWindow + payload
	mac := hmac.New(sha256.New, []byte(c.apiSecret))
	mac.Write([]byte(val))
	return hex.EncodeToString(mac.Sum(nil))
}

type apiResponse struct {
	RetCode int             `json:"retCode"`
	RetMsg  string          `json:"retMsg"`
	Result  json.RawMessage `json:"result"`
}

// get calls a GET endpoint. The query string is both sent and signed, so it
// must be encoded once (url.Values.Encode) and passed here as is.
func (c *Client) get(endpoint string, query url.Values, signed bool) (json.RawMessage, error) {
	q := query.Encode()
	req, err := http.NewRequest(http.MethodGet, c.baseURL+endpoint+"?"+q, nil)
	if err != nil {
		return nil, err
	}
	if signed {
		c.setAuthHeaders(req, q)
	}
	return c.do(req)
}

func (c *Client) post(endpoint string, body any) (json.RawMessage, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.baseURL+endpoint, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	c.setAuthHeaders(req, string(payload))
	return c.do(req)
}

func (c *Client) setAuthHeaders(req *http.Request, payload string) {
	timestamp := strconv.FormatInt(time.Now().UnixMilli(), 10)
	req.Header.Set("X-BAPI-API-KEY", c.apiKey)
	req.Header.Set("X-BAPI-TIMESTAMP", timestamp)
	req.Header.Set("X-BAPI-RECV-WINDOW", recvWindow)
	req.Header.Set("X-BAPI-SIGN", c.sign(timestamp, payload))
}

func (c *Client) do(req *http.Request) (json.RawMessage, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bybit %s: http %s - %s", req.URL.Path, resp.Status, string(body))
	}

	// Bybit answers HTTP 200 for business errors (rejected order, bad params),
	// the real status is in retCode.
	var r apiResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("bybit %s: decode response: %w", req.URL.Path, err)
	}
	if r.RetCode != 0 {
		return nil, fmt.Errorf("bybit %s: retCode %d: %s", req.URL.Path, r.RetCode, r.RetMsg)
	}

	return r.Result, nil
}

// Interval converts a config timeframe ("4h") to a Bybit kline interval ("240").
func Interval(timeframe string) (string, error) {
	switch timeframe {
	case "1m":
		return "1", nil
	case "3m":
		return "3", nil
	case "5m":
		return "5", nil
	case "15m":
		return "15", nil
	case "30m":
		return "30", nil
	case "1h":
		return "60", nil
	case "2h":
		return "120", nil
	case "4h":
		return "240", nil
	case "6h":
		return "360", nil
	case "12h":
		return "720", nil
	case "1d":
		return "D", nil
	}
	return "", fmt.Errorf("unsupported timeframe: %s", timeframe)
}

// GetKlines fetches spot candles, oldest first. The last one is the current,
// still unfinished candle. Candle.Timeframe is the config timeframe ("4h"),
// which is what the ClickHouse Enum8 column accepts.
func (c *Client) GetKlines(symbol, timeframe string, limit int) ([]db.Candle, error) {
	interval, err := Interval(timeframe)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("category", "spot")
	query.Set("symbol", symbol)
	query.Set("interval", interval)
	query.Set("limit", strconv.Itoa(limit))

	result, err := c.get("/v5/market/kline", query, false)
	if err != nil {
		return nil, err
	}

	var kr struct {
		List [][]string `json:"list"`
	}
	if err := json.Unmarshal(result, &kr); err != nil {
		return nil, err
	}

	candles := make([]db.Candle, 0, len(kr.List))
	for _, k := range kr.List {
		if len(k) < 6 {
			return nil, fmt.Errorf("kline: unexpected row %v", k)
		}

		ts, err := strconv.ParseInt(k[0], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("kline: bad timestamp %q: %w", k[0], err)
		}
		var vals [5]float64
		for i := range vals {
			if vals[i], err = strconv.ParseFloat(k[i+1], 64); err != nil {
				return nil, fmt.Errorf("kline: bad number %q: %w", k[i+1], err)
			}
		}

		candles = append(candles, db.Candle{
			Symbol:    symbol,
			Timeframe: timeframe,
			Timestamp: time.UnixMilli(ts).UTC(),
			Open:      vals[0],
			High:      vals[1],
			Low:       vals[2],
			Close:     vals[3],
			Volume:    vals[4],
		})
	}

	// Bybit returns newest first, so we reverse to have oldest first
	sort.Slice(candles, func(i, j int) bool {
		return candles[i].Timestamp.Before(candles[j].Timestamp)
	})

	return candles, nil
}

// GetInstrument loads spot trading rules (tick size, lot precision, minimums).
func (c *Client) GetInstrument(symbol string) (Instrument, error) {
	query := url.Values{}
	query.Set("category", "spot")
	query.Set("symbol", symbol)

	result, err := c.get("/v5/market/instruments-info", query, false)
	if err != nil {
		return Instrument{}, err
	}

	var ir struct {
		List []struct {
			Symbol        string `json:"symbol"`
			BaseCoin      string `json:"baseCoin"`
			QuoteCoin     string `json:"quoteCoin"`
			LotSizeFilter struct {
				BasePrecision  string `json:"basePrecision"`
				QuotePrecision string `json:"quotePrecision"`
				MinOrderQty    string `json:"minOrderQty"`
				MinOrderAmt    string `json:"minOrderAmt"`
			} `json:"lotSizeFilter"`
			PriceFilter struct {
				TickSize string `json:"tickSize"`
			} `json:"priceFilter"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &ir); err != nil {
		return Instrument{}, err
	}

	for _, it := range ir.List {
		if it.Symbol != symbol {
			continue
		}
		return NewInstrument(it.BaseCoin, it.QuoteCoin,
			it.PriceFilter.TickSize,
			it.LotSizeFilter.BasePrecision,
			it.LotSizeFilter.QuotePrecision,
			it.LotSizeFilter.MinOrderQty,
			it.LotSizeFilter.MinOrderAmt,
		)
	}
	return Instrument{}, fmt.Errorf("instrument %s not found", symbol)
}

// GetCoinBalance returns the unified account wallet balance of a coin, 0 if
// the coin is absent.
func (c *Client) GetCoinBalance(coin string) (float64, error) {
	query := url.Values{}
	query.Set("accountType", "UNIFIED")
	query.Set("coin", coin)

	result, err := c.get("/v5/account/wallet-balance", query, true)
	if err != nil {
		return 0, err
	}

	var wr struct {
		List []struct {
			Coin []struct {
				Coin          string `json:"coin"`
				WalletBalance string `json:"walletBalance"`
			} `json:"coin"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &wr); err != nil {
		return 0, err
	}

	for _, acc := range wr.List {
		for _, cb := range acc.Coin {
			if cb.Coin == coin {
				return parseOptionalFloat(cb.WalletBalance)
			}
		}
	}
	return 0, nil
}

type OrderRequest struct {
	Category     string `json:"category"`
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`
	OrderType    string `json:"orderType"`
	Qty          string `json:"qty"`
	MarketUnit   string `json:"marketUnit,omitempty"`
	TriggerPrice string `json:"triggerPrice,omitempty"`
	OrderFilter  string `json:"orderFilter,omitempty"`
	OrderLinkID  string `json:"orderLinkId,omitempty"`
}

// MarketBuyByQuote buys for quoteQty of the quote coin (USDT). Bybit v5 spot
// treats a Market Buy qty as quote coin by default; marketUnit makes it explicit.
// Spot TP/SL can only be attached to Limit orders, so exits are placed
// separately with StopMarketSell.
func MarketBuyByQuote(symbol, quoteQty, orderLinkID string) OrderRequest {
	return OrderRequest{
		Category:    "spot",
		Symbol:      symbol,
		Side:        "Buy",
		OrderType:   "Market",
		Qty:         quoteQty,
		MarketUnit:  "quoteCoin",
		OrderFilter: "Order",
		OrderLinkID: orderLinkID,
	}
}

// StopMarketSell is a spot conditional order (orderFilter=StopOrder): a market
// sell of baseQty once the last price reaches triggerPrice. Unlike tpslOrder it
// does not lock the coins until triggered, so a TP and an SL can both cover the
// whole position. marketUnit is not supported for conditional orders; a market
// sell qty is in the base coin by default.
func StopMarketSell(symbol, baseQty, triggerPrice, orderLinkID string) OrderRequest {
	return OrderRequest{
		Category:     "spot",
		Symbol:       symbol,
		Side:         "Sell",
		OrderType:    "Market",
		Qty:          baseQty,
		TriggerPrice: triggerPrice,
		OrderFilter:  "StopOrder",
		OrderLinkID:  orderLinkID,
	}
}

// PlaceOrder creates an order and returns its id. Creation is asynchronous on
// Bybit's side: the id only means the order was accepted.
func (c *Client) PlaceOrder(req OrderRequest) (string, error) {
	result, err := c.post("/v5/order/create", req)
	if err != nil {
		return "", err
	}

	var or struct {
		OrderID string `json:"orderId"`
	}
	if err := json.Unmarshal(result, &or); err != nil {
		return "", err
	}
	if or.OrderID == "" {
		return "", fmt.Errorf("order create: empty orderId")
	}
	return or.OrderID, nil
}

type Order struct {
	OrderID     string
	OrderLinkID string
	Status      string
	AvgPrice    float64
	CumExecQty  float64
}

// Closed reports whether the order reached a final status.
func (o Order) Closed() bool {
	switch o.Status {
	case "Filled", "PartiallyFilledCanceled", "Cancelled", "Rejected", "Deactivated":
		return true
	}
	return false
}

type orderJSON struct {
	OrderID     string `json:"orderId"`
	OrderLinkID string `json:"orderLinkId"`
	OrderStatus string `json:"orderStatus"`
	AvgPrice    string `json:"avgPrice"`
	CumExecQty  string `json:"cumExecQty"`
}

func (c *Client) listOrders(query url.Values) ([]Order, error) {
	result, err := c.get("/v5/order/realtime", query, true)
	if err != nil {
		return nil, err
	}

	var lr struct {
		List []orderJSON `json:"list"`
	}
	if err := json.Unmarshal(result, &lr); err != nil {
		return nil, err
	}

	orders := make([]Order, 0, len(lr.List))
	for _, o := range lr.List {
		avg, err := parseOptionalFloat(o.AvgPrice)
		if err != nil {
			return nil, fmt.Errorf("order %s: avgPrice: %w", o.OrderID, err)
		}
		qty, err := parseOptionalFloat(o.CumExecQty)
		if err != nil {
			return nil, fmt.Errorf("order %s: cumExecQty: %w", o.OrderID, err)
		}
		orders = append(orders, Order{
			OrderID:     o.OrderID,
			OrderLinkID: o.OrderLinkID,
			Status:      o.OrderStatus,
			AvgPrice:    avg,
			CumExecQty:  qty,
		})
	}
	return orders, nil
}

// GetClosedOrder looks an order up among recently closed ones (openOnly=1:
// filled, cancelled, rejected). found is false while the order is still open
// or not yet visible.
func (c *Client) GetClosedOrder(symbol, orderID string) (order Order, found bool, err error) {
	query := url.Values{}
	query.Set("category", "spot")
	query.Set("symbol", symbol)
	query.Set("orderId", orderID)
	query.Set("openOnly", "1")

	orders, err := c.listOrders(query)
	if err != nil {
		return Order{}, false, err
	}
	for _, o := range orders {
		if o.OrderID == orderID {
			return o, true, nil
		}
	}
	return Order{}, false, nil
}

// GetOpenStopOrders lists untriggered spot conditional orders of a symbol.
func (c *Client) GetOpenStopOrders(symbol string) ([]Order, error) {
	query := url.Values{}
	query.Set("category", "spot")
	query.Set("symbol", symbol)
	query.Set("orderFilter", "StopOrder")
	query.Set("limit", "50") // spot allows at most 30 open conditional orders

	return c.listOrders(query)
}

func (c *Client) CancelStopOrder(symbol, orderID string) error {
	_, err := c.post("/v5/order/cancel", map[string]string{
		"category":    "spot",
		"symbol":      symbol,
		"orderId":     orderID,
		"orderFilter": "StopOrder",
	})
	return err
}

// parseOptionalFloat parses Bybit numeric strings; UTA returns "" instead of 0
// for some fields (e.g. avgPrice of an unfilled order).
func parseOptionalFloat(s string) (float64, error) {
	if s == "" {
		return 0, nil
	}
	return strconv.ParseFloat(s, 64)
}
