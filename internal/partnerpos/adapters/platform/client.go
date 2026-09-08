// Package platform is the outbound adapter of partner-pos. It talks to the
// platform over the same public HTTP APIs any outlet uses, with the access key
// of the outlet in X-Api-Key. It holds no business rules.
//
// There is no gRPC here on purpose: partner-pos models an external
// organisation (TZ v3.0 §3.3) and has no access to the internal network.
package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/avito-kitchen/platform/internal/partnerpos/app"
	"github.com/avito-kitchen/platform/internal/partnerpos/domain"
)

// Config addresses the three public APIs the till uses.
type Config struct {
	CatalogBaseURL   string
	InventoryBaseURL string
	OrderBaseURL     string
	APIKey           string
	// Timeout bounds one HTTP call, so a slow platform cannot block a ticker.
	Timeout time.Duration
}

// Client is the platform adapter.
type Client struct {
	cfg  Config
	http *http.Client
}

// NewClient builds the adapter over an http.Client with a bounded timeout.
func NewClient(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 5 * time.Second
	}
	return &Client{cfg: cfg, http: &http.Client{Timeout: cfg.Timeout}}
}

// ---------------------------------------------------------------- catalogue

type publishItem struct {
	ExternalID  string `json:"external_id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	PriceMinor  int64  `json:"price_minor"`
	Position    int32  `json:"position"`
}

type publishCategory struct {
	Name     string        `json:"name"`
	Position int32         `json:"position"`
	Items    []publishItem `json:"items"`
}

type publishRequest struct {
	Categories []publishCategory `json:"categories"`
}

type menuResponse struct {
	MenuVersion int64 `json:"menu_version"`
	Categories  []struct {
		Items []struct {
			MenuItemID string `json:"menu_item_id"`
			ExternalID string `json:"external_id"`
		} `json:"items"`
	} `json:"categories"`
}

// PublishMenu replaces the published menu with the till menu.
//
// The current version is read first and sent back in If-Match, so a menu
// changed by someone else in the meantime is refused with 412 instead of being
// silently overwritten. Version 0 means the outlet has never published.
func (c *Client) PublishMenu(ctx context.Context, menu domain.Menu) (int64, error) {
	version, _, err := c.FetchMenu(ctx)
	if err != nil {
		return 0, err
	}

	body := publishRequest{}
	for _, category := range menu.Categories() {
		out := publishCategory{Name: category.Name, Position: category.Position}
		for _, item := range category.Items {
			out.Items = append(out.Items, publishItem{
				ExternalID: item.ExternalID, Name: item.Name,
				Description: item.Description, PriceMinor: item.PriceMinor,
				Position: item.Position,
			})
		}
		body.Categories = append(body.Categories, out)
	}

	var published menuResponse
	headers := map[string]string{"If-Match": strconv.FormatInt(version, 10)}
	if err := c.call(ctx, http.MethodPut, c.cfg.CatalogBaseURL,
		"/api/v1/merchant/menu", headers, body, &published); err != nil {
		return 0, app.Unavailable("catalog publish menu", err)
	}
	return published.MenuVersion, nil
}

// FetchMenu reads the published menu back and returns the identifier mapping.
func (c *Client) FetchMenu(ctx context.Context) (int64, []app.PublishedItem, error) {
	var menu menuResponse
	if err := c.call(ctx, http.MethodGet, c.cfg.CatalogBaseURL,
		"/api/v1/merchant/menu", nil, nil, &menu); err != nil {
		return 0, nil, app.Unavailable("catalog read menu", err)
	}
	var items []app.PublishedItem
	for _, category := range menu.Categories {
		for _, item := range category.Items {
			items = append(items, app.PublishedItem{
				ExternalID: item.ExternalID, MenuItemID: item.MenuItemID,
			})
		}
	}
	return menu.MenuVersion, items, nil
}

// ---------------------------------------------------------------- inventory

type setStockRequest struct {
	OnHandQty int32  `json:"on_hand_qty"`
	Reason    string `json:"reason"`
}

type salesStateRequest struct {
	SalesState string `json:"sales_state"`
}

// SetStock assigns the absolute number of units of one position.
func (c *Client) SetStock(ctx context.Context, menuItemID string, qty int32, reason string) error {
	body := setStockRequest{OnHandQty: qty, Reason: reason}
	if err := c.call(ctx, http.MethodPut, c.cfg.InventoryBaseURL,
		"/api/v1/merchant/stock/"+url.PathEscape(menuItemID), nil, body, nil); err != nil {
		return app.Unavailable("inventory set stock", err)
	}
	return nil
}

// SetSalesState puts one position on the stop list or takes it off.
func (c *Client) SetSalesState(ctx context.Context, menuItemID string, stopped bool) error {
	state := "ENABLED"
	if stopped {
		state = "STOPPED"
	}
	body := salesStateRequest{SalesState: state}
	if err := c.call(ctx, http.MethodPatch, c.cfg.InventoryBaseURL,
		"/api/v1/merchant/stock/"+url.PathEscape(menuItemID), nil, body, nil); err != nil {
		return app.Unavailable("inventory set sales state", err)
	}
	return nil
}

// ------------------------------------------------------------------- orders

type orderPageResponse struct {
	Items []struct {
		OrderID    string `json:"order_id"`
		Status     string `json:"status"`
		TotalMinor int64  `json:"total_minor"`
		ETAMinutes int32  `json:"eta_minutes"`
		UpdatedAt  string `json:"updated_at"`
		Items      []struct {
			MenuItemID string `json:"menu_item_id"`
			ExternalID string `json:"external_id"`
			Name       string `json:"name"`
			Qty        int32  `json:"qty"`
		} `json:"items"`
	} `json:"items"`
	Total int `json:"total"`
}

type acceptRequest struct {
	ETAMinutes int32 `json:"eta_minutes"`
}

type rejectRequest struct {
	Reason string `json:"reason"`
}

type statusRequest struct {
	Status string `json:"status"`
}

// ListOrders returns the orders of the outlet in the given statuses.
func (c *Client) ListOrders(ctx context.Context, statuses []string, limit int) ([]app.Order, error) {
	query := url.Values{}
	if len(statuses) > 0 {
		query.Set("status", strings.Join(statuses, ","))
	}
	query.Set("limit", strconv.Itoa(limit))

	var page orderPageResponse
	if err := c.call(ctx, http.MethodGet, c.cfg.OrderBaseURL,
		"/api/v1/merchant/orders?"+query.Encode(), nil, nil, &page); err != nil {
		return nil, app.Unavailable("order list queue", err)
	}
	out := make([]app.Order, 0, len(page.Items))
	for _, o := range page.Items {
		order := app.Order{
			ID: o.OrderID, Status: o.Status, TotalMinor: o.TotalMinor,
			ETAMinutes: o.ETAMinutes, UpdatedAt: o.UpdatedAt,
		}
		for _, item := range o.Items {
			order.Items = append(order.Items, app.OrderItem{
				MenuItemID: item.MenuItemID, ExternalID: item.ExternalID,
				Name: item.Name, Qty: item.Qty,
			})
		}
		out = append(out, order)
	}
	return out, nil
}

// AcceptOrder confirms an order.
func (c *Client) AcceptOrder(ctx context.Context, orderID string, etaMinutes int32) error {
	if err := c.call(ctx, http.MethodPost, c.cfg.OrderBaseURL,
		"/api/v1/merchant/orders/"+url.PathEscape(orderID)+"/accept", nil,
		acceptRequest{ETAMinutes: etaMinutes}, nil); err != nil {
		return app.Unavailable("order accept", err)
	}
	return nil
}

// RejectOrder refuses an order.
func (c *Client) RejectOrder(ctx context.Context, orderID, reason string) error {
	if err := c.call(ctx, http.MethodPost, c.cfg.OrderBaseURL,
		"/api/v1/merchant/orders/"+url.PathEscape(orderID)+"/reject", nil,
		rejectRequest{Reason: reason}, nil); err != nil {
		return app.Unavailable("order reject", err)
	}
	return nil
}

// AdvanceOrder moves a confirmed order one step along preparation.
func (c *Client) AdvanceOrder(ctx context.Context, orderID, status string) error {
	if err := c.call(ctx, http.MethodPost, c.cfg.OrderBaseURL,
		"/api/v1/merchant/orders/"+url.PathEscape(orderID)+"/status", nil,
		statusRequest{Status: status}, nil); err != nil {
		return app.Unavailable("order advance", err)
	}
	return nil
}

// ------------------------------------------------------------------ transport

// maxErrorBody caps how much of a failed response is read into the error, so a
// misbehaving peer cannot make the till allocate without bound.
const maxErrorBody = 4 << 10

// call performs one JSON request. Any status outside 2xx is an error: the till
// has no business interpretation for a refusal from the platform, it simply
// retries on the next tick.
func (c *Client) call(ctx context.Context, method, baseURL, path string,
	headers map[string]string, in, out any) error {
	var body io.Reader
	if in != nil {
		encoded, err := json.Marshal(in)
		if err != nil {
			return fmt.Errorf("encode request: %w", err)
		}
		body = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method,
		strings.TrimRight(baseURL, "/")+path, body)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Api-Key", c.cfg.APIKey)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxErrorBody))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
		return fmt.Errorf("%s %s: status %d: %s", method, path, resp.StatusCode,
			strings.TrimSpace(string(snippet)))
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode response: %w", method, path, err)
	}
	return nil
}

// the adapter must keep satisfying the port it is written for.
var _ app.Platform = (*Client)(nil)
