// Package upstream turns a declarative HTTP service description into
// outbound requests. It owns everything that is easy to get wrong: path
// escaping, prefix allowlists, redirects, timeouts, body limits and the
// fact that a credential is read from the environment, never from the config.
package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"mcpx/internal/config"
	"mcpx/internal/tool"
)

// MaxRedirects bounds a chain: a gateway is not a browser.
const MaxRedirects = 3

// Client is one configured upstream. It is immutable after construction and
// safe for concurrent use.
type Client struct {
	name             string
	base             *url.URL
	basePath         string
	http             *http.Client
	headers          http.Header
	auth             config.Auth
	maxResponseBytes int64
	maxBodyBytes     int64
}

// ClientOptions is the normalised form of an upstream config block.
type ClientOptions struct {
	Name             string
	BaseURL          string
	Headers          map[string]string
	Auth             *config.Auth
	Timeout          time.Duration
	MaxResponseBytes int64
	MaxBodyBytes     int64
}

// NewClient validates the address once and builds a dedicated HTTP client with
// a shared, tuned transport.
func NewClient(opts ClientOptions) (*Client, error) {
	base, err := url.Parse(strings.TrimSpace(opts.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("upstream %s: base_url: %w", opts.Name, err)
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return nil, fmt.Errorf("upstream %s: base_url must be http or https", opts.Name)
	}
	if base.Host == "" {
		return nil, fmt.Errorf("upstream %s: base_url must include a host", opts.Name)
	}
	if base.User != nil {
		return nil, fmt.Errorf("upstream %s: base_url must not embed credentials", opts.Name)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = config.DefaultTimeout
	}
	maxResponse := opts.MaxResponseBytes
	if maxResponse <= 0 {
		maxResponse = config.DefaultMaxResponseBytes
	}
	maxBody := opts.MaxBodyBytes
	if maxBody <= 0 {
		maxBody = config.DefaultMaxBodyBytes
	}

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	client := &Client{
		name:             opts.Name,
		base:             base,
		basePath:         strings.TrimSuffix(base.Path, "/"),
		headers:          make(http.Header, len(opts.Headers)),
		maxResponseBytes: maxResponse,
		maxBodyBytes:     maxBody,
		http: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			// A redirect to another host would send the configured credential
			// somewhere it was never meant to go.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= MaxRedirects {
					return fmt.Errorf("too many redirects")
				}
				if req.URL.Scheme != base.Scheme || !strings.EqualFold(req.URL.Host, base.Host) {
					return fmt.Errorf("redirect to a different origin is not allowed")
				}
				return nil
			},
		},
	}
	if opts.Auth != nil {
		client.auth = *opts.Auth
	}
	for name, value := range opts.Headers {
		client.headers.Set(name, value)
	}
	return client, nil
}

// Name is the upstream name used in tool names and error text.
func (c *Client) Name() string { return c.name }

// BaseURL is the normalised base address, for diagnostics.
func (c *Client) BaseURL() string { return c.base.String() }

// Request is one outbound call. Path is already expanded and escaped.
type Request struct {
	Method      string
	Path        string
	Query       url.Values
	Headers     http.Header
	Body        []byte
	ContentType string
}

// Response is a bounded upstream answer.
type Response struct {
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
	Truncated  bool
}

// Do performs the call. Every failure is an error, never a silent empty body.
func (c *Client) Do(ctx context.Context, req Request) (*Response, error) {
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if method == "" {
		method = http.MethodGet
	}
	if !methodAllowed(method) {
		return nil, fmt.Errorf("%w: %s", ErrMethodNotAllowed, method)
	}
	target, err := c.resolve(req.Path, req.Query)
	if err != nil {
		return nil, err
	}
	if int64(len(req.Body)) > c.maxBodyBytes {
		return nil, fmt.Errorf("%w: %d bytes exceeds %d", ErrRequestBodyTooLarge, len(req.Body), c.maxBodyBytes)
	}

	var body io.Reader
	if len(req.Body) > 0 {
		body = bytes.NewReader(req.Body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("upstream %s: build request: %w", c.name, err)
	}
	for name, values := range c.headers {
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	for name, values := range req.Headers {
		if isReservedHeader(name) {
			continue
		}
		for _, value := range values {
			httpReq.Header.Add(name, value)
		}
	}
	if httpReq.Header.Get("Accept") == "" {
		httpReq.Header.Set("Accept", "application/json, text/plain;q=0.9, */*;q=0.5")
	}
	httpReq.Header.Set("User-Agent", "mcpx")
	if len(req.Body) > 0 {
		contentType := req.ContentType
		if contentType == "" {
			contentType = httpReq.Header.Get("Content-Type")
		}
		if contentType == "" {
			contentType = "application/json"
		}
		httpReq.Header.Set("Content-Type", contentType)
	}
	if err := c.applyAuth(httpReq); err != nil {
		return nil, err
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("%w: upstream %s did not answer in time", ErrTimeout, c.name)
		case errors.Is(err, context.Canceled):
			return nil, fmt.Errorf("upstream %s: canceled: %w", c.name, err)
		default:
			return nil, fmt.Errorf("%w: upstream %s: %v", ErrUnreachable, c.name, err)
		}
	}
	defer func() { _ = resp.Body.Close() }()

	limited := io.LimitReader(resp.Body, c.maxResponseBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("%w: upstream %s: reading body failed", ErrUnreachable, c.name)
	}
	truncated := false
	if int64(len(data)) > c.maxResponseBytes {
		data = data[:c.maxResponseBytes]
		truncated = true
	}
	return &Response{
		StatusCode: resp.StatusCode,
		Status:     resp.Status,
		Header:     resp.Header.Clone(),
		Body:       data,
		Truncated:  truncated,
	}, nil
}

// resolve joins the configured base path with the request path and proves the
// result still points at the configured origin.
func (c *Client) resolve(path string, query url.Values) (*url.URL, error) {
	if strings.TrimSpace(path) == "" {
		path = "/"
	}
	if err := ValidatePath(path); err != nil {
		return nil, err
	}
	full := c.basePath + path
	plain, err := url.PathUnescape(full)
	if err != nil {
		return nil, fmt.Errorf("%w: %q is not a valid path", ErrPathNotAllowed, full)
	}
	if c.basePath != "" && plain != c.basePath && !strings.HasPrefix(plain, c.basePath+"/") {
		return nil, fmt.Errorf("%w: %q escapes the base path", ErrPathNotAllowed, plain)
	}
	target := &url.URL{
		Scheme:   c.base.Scheme,
		Host:     c.base.Host,
		Path:     plain,
		RawPath:  full,
		RawQuery: query.Encode(),
	}
	if target.Scheme != c.base.Scheme || !strings.EqualFold(target.Host, c.base.Host) {
		return nil, fmt.Errorf("%w: %q leaves the configured origin", ErrPathNotAllowed, path)
	}
	return target, nil
}

// applyAuth injects the configured credential. A missing environment variable
// is a hard failure: sending an unauthenticated request would look like a
// permission problem at the upstream and waste the operator's time.
func (c *Client) applyAuth(req *http.Request) error {
	switch c.auth.Scheme {
	case "", config.SchemeNone:
		return nil
	case config.SchemeBearer:
		token, err := c.token()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	case config.SchemeHeader:
		token, err := c.token()
		if err != nil {
			return err
		}
		req.Header.Set(c.auth.HeaderName, token)
		return nil
	default:
		return fmt.Errorf("upstream %s: unsupported auth scheme %q", c.name, c.auth.Scheme)
	}
}

func (c *Client) token() (string, error) {
	name := strings.TrimSpace(c.auth.TokenEnv)
	if name == "" {
		return "", fmt.Errorf("%w: upstream %s has no token_env", ErrCredentialsMissing, c.name)
	}
	token := strings.TrimSpace(os.Getenv(name))
	if token == "" {
		return "", fmt.Errorf("%w: upstream %s: environment variable %s is empty", ErrCredentialsMissing, c.name, name)
	}
	return token, nil
}

// methodAllowed mirrors the config whitelist; it is re-checked here so a
// passthrough cannot invent a method the config never permitted.
func methodAllowed(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// ResultFromResponse renders the upstream answer as an MCP tool result. A
// non-2xx status is an isError result, not a protocol failure: the model needs
// to read the status and body to decide what to do next.
func ResultFromResponse(upstreamName string, resp *Response) tool.Result {
	body := strings.TrimSpace(string(resp.Body))
	if body == "" {
		body = "(empty body)"
	}
	if resp.Truncated {
		body += fmt.Sprintf("\n[truncated at %d bytes]", len(resp.Body))
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tool.Result{
			Text:    fmt.Sprintf("upstream %s returned %s\n%s", upstreamName, resp.Status, body),
			IsError: true,
		}
	}
	result := tool.Result{Text: body}
	if json.Valid(resp.Body) {
		var decoded any
		if err := json.Unmarshal(resp.Body, &decoded); err == nil {
			result.Structured = decoded
			if len(resp.Body) <= 64<<10 && !resp.Truncated {
				if pretty, err := json.MarshalIndent(decoded, "", "  "); err == nil {
					result.Text = string(pretty)
				}
			}
		}
	}
	return result
}
