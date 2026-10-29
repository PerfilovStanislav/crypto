package exchange

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"trader/config"
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

// APIError is a business error Bybit reports with HTTP 200 and retCode != 0.
type APIError struct {
	Path string
	Code int
	Msg  string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("bybit %s: retCode %d: %s", e.Path, e.Code, e.Msg)
}

func isRetCode(err error, code int) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Code == code
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
		return nil, &APIError{Path: req.URL.Path, Code: r.RetCode, Msg: r.RetMsg}
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

// Candle is the OHLCV of the candle that started at Timestamp (UTC).
type Candle struct {
	Timestamp time.Time
	Low       float64
	Open      float64
	Close     float64
	High      float64
	Volume    float64
}

// GetKlines fetches USDT perpetual candles, oldest first: the market the bot
// trades and the analyzer backtests. The last candle is the current, still
// unfinished one.
func (c *Client) GetKlines(symbol, timeframe string, limit int) ([]Candle, error) {
	interval, err := Interval(timeframe)
	if err != nil {
		return nil, err
	}

	query := url.Values{}
	query.Set("category", "linear")
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

	candles := make([]Candle, 0, len(kr.List))
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

		candles = append(candles, Candle{
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

// GetInstrument loads USDT perpetual trading rules (tick size, qty step, minimums).
func (c *Client) GetInstrument(symbol string) (Instrument, error) {
	query := url.Values{}
	query.Set("category", "linear")
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
				QtyStep          string `json:"qtyStep"`
				MinOrderQty      string `json:"minOrderQty"`
				MinNotionalValue string `json:"minNotionalValue"`
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
			it.LotSizeFilter.QtyStep,
			it.LotSizeFilter.MinOrderQty,
			it.LotSizeFilter.MinNotionalValue,
		)
	}
	return Instrument{}, fmt.Errorf("instrument %s not found", symbol)
}

// SetupHedge switches a USDT perpetual to hedge mode, where a long and a short
// are separate positions (positionIdx 1 and 2), and sets leverage 1x on both
// sides, so a position never needs more margin than its value. Both calls are
// no-ops when already set (retCodes 110025 and 110043).
func (c *Client) SetupHedge(symbol string) error {
	_, err := c.post("/v5/position/switch-mode", map[string]any{
		"category": "linear",
		"symbol":   symbol,
		"mode":     3,
	})
	if err != nil && !isRetCode(err, 110025) {
		return fmt.Errorf("switch %s to hedge mode: %w", symbol, err)
	}
	_, err = c.post("/v5/position/set-leverage", map[string]string{
		"category":     "linear",
		"symbol":       symbol,
		"buyLeverage":  "1",
		"sellLeverage": "1",
	})
	if err != nil && !isRetCode(err, 110043) {
		return fmt.Errorf("set %s leverage 1x: %w", symbol, err)
	}
	return nil
}

// Position is one side of a hedge-mode position. Size is in the base coin, 0
// means flat; StopLoss is 0 when the position has none.
type Position struct {
	Size     float64
	StopLoss float64
}

// GetPosition returns one side of a hedge-mode position: positionIdx 1 is the
// long, 2 the short.
func (c *Client) GetPosition(symbol string, positionIdx int) (Position, error) {
	query := url.Values{}
	query.Set("category", "linear")
	query.Set("symbol", symbol)

	result, err := c.get("/v5/position/list", query, true)
	if err != nil {
		return Position{}, err
	}

	var pr struct {
		List []struct {
			Symbol      string `json:"symbol"`
			PositionIdx int    `json:"positionIdx"`
			Size        string `json:"size"`
			StopLoss    string `json:"stopLoss"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &pr); err != nil {
		return Position{}, err
	}
	for _, p := range pr.List {
		if p.Symbol == symbol && p.PositionIdx == positionIdx {
			size, err := parseOptionalFloat(p.Size)
			if err != nil {
				return Position{}, fmt.Errorf("size: %w", err)
			}
			sl, err := parseOptionalFloat(p.StopLoss)
			if err != nil {
				return Position{}, fmt.Errorf("stopLoss: %w", err)
			}
			return Position{Size: size, StopLoss: sl}, nil
		}
	}
	return Position{}, nil
}

// SetTradingStop puts TP and SL on one side of a hedge-mode position (Full
// mode: each closes the whole position at market, triggered by the last
// price). They belong to the position, so when one fires Bybit cancels the
// other.
func (c *Client) SetTradingStop(symbol string, positionIdx int, takeProfit, stopLoss string) error {
	_, err := c.post("/v5/position/trading-stop", map[string]any{
		"category":    "linear",
		"symbol":      symbol,
		"tpslMode":    "Full",
		"positionIdx": positionIdx,
		"takeProfit":  takeProfit,
		"stopLoss":    stopLoss,
		"tpTriggerBy": "LastPrice",
		"slTriggerBy": "LastPrice",
	})
	return err
}

// GetFreeBalance returns how much of a coin the unified account can still put
// into new orders: the wallet balance minus the margin held by open positions
// and orders (on futures the wallet balance does not drop when a position
// opens). 0 if the coin is absent.
func (c *Client) GetFreeBalance(coin string) (float64, error) {
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
				Coin            string `json:"coin"`
				WalletBalance   string `json:"walletBalance"`
				TotalPositionIM string `json:"totalPositionIM"`
				TotalOrderIM    string `json:"totalOrderIM"`
			} `json:"coin"`
		} `json:"list"`
	}
	if err := json.Unmarshal(result, &wr); err != nil {
		return 0, err
	}

	for _, acc := range wr.List {
		for _, cb := range acc.Coin {
			if cb.Coin != coin {
				continue
			}
			var v [3]float64
			for i, s := range []string{cb.WalletBalance, cb.TotalPositionIM, cb.TotalOrderIM} {
				if v[i], err = parseOptionalFloat(s); err != nil {
					return 0, fmt.Errorf("%s balance: %w", coin, err)
				}
			}
			return v[0] - v[1] - v[2], nil
		}
	}
	return 0, nil
}

type OrderRequest struct {
	Category    string `json:"category"`
	Symbol      string `json:"symbol"`
	Side        string `json:"side"`
	OrderType   string `json:"orderType"`
	Qty         string `json:"qty"`
	PositionIdx int    `json:"positionIdx"`
	ReduceOnly  bool   `json:"reduceOnly,omitempty"`
	OrderLinkID string `json:"orderLinkId,omitempty"`
}

// MarketOrder is a USDT perpetual market order for qty of the base coin on the
// hedge-mode side positionIdx: it opens (or, with reduceOnly, closes) that side.
func MarketOrder(symbol, side, qty string, positionIdx int, reduceOnly bool, orderLinkID string) OrderRequest {
	return OrderRequest{
		Category:    "linear",
		Symbol:      symbol,
		Side:        side,
		OrderType:   "Market",
		Qty:         qty,
		PositionIdx: positionIdx,
		ReduceOnly:  reduceOnly,
		OrderLinkID: orderLinkID,
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
	PositionIdx int
	// StopOrderType is "StopLoss" or "TakeProfit" for the position's TP/SL,
	// "Stop" for a plain conditional order, "" for an ordinary one.
	StopOrderType string
	UpdatedTime   time.Time
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
	OrderID       string `json:"orderId"`
	OrderLinkID   string `json:"orderLinkId"`
	OrderStatus   string `json:"orderStatus"`
	AvgPrice      string `json:"avgPrice"`
	CumExecQty    string `json:"cumExecQty"`
	PositionIdx   int    `json:"positionIdx"`
	StopOrderType string `json:"stopOrderType"`
	UpdatedTime   string `json:"updatedTime"`
}

func (c *Client) listOrders(endpoint string, query url.Values) ([]Order, error) {
	result, err := c.get(endpoint, query, true)
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
		updated, err := parseOptionalFloat(o.UpdatedTime)
		if err != nil {
			return nil, fmt.Errorf("order %s: updatedTime: %w", o.OrderID, err)
		}
		orders = append(orders, Order{
			OrderID:       o.OrderID,
			OrderLinkID:   o.OrderLinkID,
			Status:        o.OrderStatus,
			AvgPrice:      avg,
			CumExecQty:    qty,
			PositionIdx:   o.PositionIdx,
			StopOrderType: o.StopOrderType,
			UpdatedTime:   time.UnixMilli(int64(updated)).UTC(),
		})
	}
	return orders, nil
}

// GetClosedOrder looks an order up among recently closed ones (openOnly=1:
// filled, cancelled, rejected). found is false while the order is still open
// or not yet visible.
func (c *Client) GetClosedOrder(symbol, orderID string) (order Order, found bool, err error) {
	query := url.Values{}
	query.Set("category", "linear")
	query.Set("symbol", symbol)
	query.Set("orderId", orderID)
	query.Set("openOnly", "1")

	orders, err := c.listOrders("/v5/order/realtime", query)
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

// GetOpenStopOrders lists untriggered conditional orders of a USDT perpetual.
func (c *Client) GetOpenStopOrders(symbol string) ([]Order, error) {
	query := url.Values{}
	query.Set("category", "linear")
	query.Set("symbol", symbol)
	query.Set("orderFilter", "StopOrder")
	query.Set("limit", "50") // the maximum page

	return c.listOrders("/v5/order/realtime", query)
}

// GetFilledOrders lists the latest filled orders of a USDT perpetual, newest
// first; Bybit keeps them in the history for 7 days.
func (c *Client) GetFilledOrders(symbol string) ([]Order, error) {
	query := url.Values{}
	query.Set("category", "linear")
	query.Set("symbol", symbol)
	query.Set("orderStatus", "Filled")
	query.Set("limit", "50") // the maximum page

	return c.listOrders("/v5/order/history", query)
}

func (c *Client) CancelStopOrder(symbol, orderID string) error {
	_, err := c.post("/v5/order/cancel", map[string]string{
		"category": "linear",
		"symbol":   symbol,
		"orderId":  orderID,
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
