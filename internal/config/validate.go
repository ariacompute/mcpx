package config

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	upstreamNameRe  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	operationNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`)
	paramNameRe     = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,63}$`)
	envNameRe       = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	templateRe      = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)
	headerNameRe    = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
)

var allowedMethods = map[string]bool{
	"GET": true, "HEAD": true, "POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

var bodyMethods = map[string]bool{
	"POST": true, "PUT": true, "PATCH": true, "DELETE": true,
}

var jsonTypes = map[string]bool{
	"string": true, "number": true, "integer": true, "boolean": true,
	"object": true, "array": true, "null": true,
}

// Validate rejects anything the gateway could not serve safely. The first
// problem is returned with its location, because a config error at startup
// must be actionable without reading the source.
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("config: nil")
	}
	if len(c.Upstreams) == 0 {
		return ErrNoUpstreams
	}
	if strings.TrimSpace(c.Server.Listen) == "" {
		return fmt.Errorf("server.listen must not be empty")
	}
	if !strings.HasPrefix(c.Server.Path, "/") {
		return fmt.Errorf("server.path %q must start with /", c.Server.Path)
	}
	if strings.ContainsAny(c.Server.Path, " ?#") || strings.Contains(c.Server.Path, "//") {
		return fmt.Errorf("server.path %q must be a plain path", c.Server.Path)
	}
	if c.Server.MaxRequestBytes <= 0 {
		return fmt.Errorf("server.max_request_bytes must be positive")
	}
	if c.Server.SessionTTL.Std() <= 0 {
		return fmt.Errorf("server.session_ttl must be positive")
	}
	for _, v := range c.Server.ProtocolVersions {
		if strings.TrimSpace(v) == "" {
			return fmt.Errorf("server.protocol_versions contains an empty entry")
		}
	}
	if c.Server.Auth != nil {
		if err := validateAuth("server.auth", c.Server.Auth); err != nil {
			return err
		}
		if c.Server.Auth.Scheme != "" && c.Server.Auth.Scheme != SchemeNone && c.Server.Auth.Scheme != SchemeBearer {
			return fmt.Errorf("server.auth.scheme %q must be none or bearer", c.Server.Auth.Scheme)
		}
	}
	for _, origin := range c.Server.AllowedOrigins {
		if err := validateOrigin(origin); err != nil {
			return err
		}
	}
	if c.Defaults.Timeout.Std() <= 0 {
		return fmt.Errorf("defaults.timeout must be positive")
	}
	if c.Defaults.MaxResponseBytes <= 0 {
		return fmt.Errorf("defaults.max_response_bytes must be positive")
	}
	if c.Defaults.MaxBodyBytes <= 0 {
		return fmt.Errorf("defaults.max_body_bytes must be positive")
	}

	seen := make(map[string]bool, len(c.Upstreams))
	toolOwners := make(map[string]string)
	for i := range c.Upstreams {
		if err := c.validateUpstream(&c.Upstreams[i], toolOwners, seen); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateUpstream(u *Upstream, toolOwners map[string]string, seen map[string]bool) error {
	if !upstreamNameRe.MatchString(u.Name) {
		return fmt.Errorf("upstream %q: name must match %s", u.Name, upstreamNameRe)
	}
	if seen[u.Name] {
		return fmt.Errorf("upstream %q declared more than once", u.Name)
	}
	seen[u.Name] = true

	base, err := url.Parse(u.BaseURL)
	if err != nil {
		return fmt.Errorf("upstream %s: base_url: %w", u.Name, err)
	}
	switch {
	case base.Scheme != "http" && base.Scheme != "https":
		return fmt.Errorf("upstream %s: base_url must be http or https", u.Name)
	case base.Host == "":
		return fmt.Errorf("upstream %s: base_url must include a host", u.Name)
	case base.User != nil:
		return fmt.Errorf("upstream %s: base_url must not embed credentials", u.Name)
	case base.RawQuery != "" || base.Fragment != "":
		return fmt.Errorf("upstream %s: base_url must not carry a query or fragment", u.Name)
	case strings.Contains(base.Path, ".."):
		return fmt.Errorf("upstream %s: base_url must not contain ..", u.Name)
	}
	if u.Timeout.Std() <= 0 {
		return fmt.Errorf("upstream %s: timeout must be positive", u.Name)
	}
	if u.MaxResponseBytes <= 0 {
		return fmt.Errorf("upstream %s: max_response_bytes must be positive", u.Name)
	}
	if err := validateHeaders("upstream "+u.Name+".headers", u.Headers); err != nil {
		return err
	}
	if u.Auth != nil {
		if err := validateAuth("upstream "+u.Name+".auth", u.Auth); err != nil {
			return err
		}
	}
	for _, prefix := range u.AllowPrefixes {
		if err := validatePrefix(prefix); err != nil {
			return fmt.Errorf("upstream %s: %w", u.Name, err)
		}
	}
	for _, op := range u.Operations {
		if err := validateOperation(u.Name, op, toolOwners); err != nil {
			return err
		}
	}

	if u.Passthrough != nil && u.Passthrough.Enabled {
		if !operationNameRe.MatchString(u.Passthrough.Name) {
			return fmt.Errorf("upstream %s: passthrough.name %q must match %s", u.Name, u.Passthrough.Name, operationNameRe)
		}
		if err := claimTool(toolOwners, u.Name+" passthrough", u.Passthrough.Name); err != nil {
			return err
		}
		if len(u.Passthrough.Methods) == 0 {
			return fmt.Errorf("upstream %s: passthrough.methods must not be empty", u.Name)
		}
		for _, m := range u.Passthrough.Methods {
			if !allowedMethods[m] {
				return fmt.Errorf("upstream %s: passthrough method %q is not allowed", u.Name, m)
			}
		}
		if len(u.Passthrough.AllowPrefixes) == 0 {
			return fmt.Errorf("upstream %s: passthrough needs allow_prefixes (upstream.allow_prefixes or passthrough.allow_prefixes)", u.Name)
		}
		for _, prefix := range u.Passthrough.AllowPrefixes {
			if err := validatePrefix(prefix); err != nil {
				return fmt.Errorf("upstream %s: passthrough %w", u.Name, err)
			}
		}
	}

	if len(u.Operations) == 0 && (u.Passthrough == nil || !u.Passthrough.Enabled) {
		return fmt.Errorf("upstream %s: needs at least one operation or an enabled passthrough", u.Name)
	}
	return nil
}

func validateOperation(upstream string, op Operation, toolOwners map[string]string) error {
	where := "upstream " + upstream + " operation " + op.Name
	if !operationNameRe.MatchString(op.Name) {
		return fmt.Errorf("%s: name must match %s", where, operationNameRe)
	}
	if !allowedMethods[op.Method] {
		return fmt.Errorf("%s: method %q is not one of GET/HEAD/POST/PUT/PATCH/DELETE", where, op.Method)
	}
	if !strings.HasPrefix(op.Path, "/") {
		return fmt.Errorf("%s: path %q must start with /", where, op.Path)
	}
	if err := validatePathSyntax(op.Path); err != nil {
		return fmt.Errorf("%s: %w", where, err)
	}
	placeholders := templateRe.FindAllStringSubmatch(op.Path, -1)
	if residual := templateRe.ReplaceAllString(op.Path, ""); strings.ContainsAny(residual, "{}") {
		return fmt.Errorf("%s: path %q has a malformed placeholder", where, op.Path)
	}
	if err := claimTool(toolOwners, where, ToolName(upstream, op.Name)); err != nil {
		return err
	}
	if err := validateHeaders(where+".headers", op.Headers); err != nil {
		return err
	}

	declared := make(map[string]Param, len(op.Params))
	pathParams := make(map[string]bool)
	bodyParams := 0
	for _, p := range op.Params {
		if !paramNameRe.MatchString(p.Name) {
			return fmt.Errorf("%s: param %q must match %s", where, p.Name, paramNameRe)
		}
		if _, dup := declared[p.Name]; dup {
			return fmt.Errorf("%s: param %q declared twice", where, p.Name)
		}
		declared[p.Name] = p
		switch p.In {
		case ParamInPath:
			pathParams[p.Name] = true
		case ParamInQuery:
		case ParamInHeader:
			if !headerNameRe.MatchString(p.Name) {
				return fmt.Errorf("%s: header param %q is not a valid header name", where, p.Name)
			}
		case ParamInBody:
			bodyParams++
		default:
			return fmt.Errorf("%s: param %q has in=%q, expected path/query/header/body", where, p.Name, p.In)
		}
		if p.Schema != nil {
			if raw, ok := p.Schema["type"]; ok {
				name, ok := raw.(string)
				if !ok || !jsonTypes[name] {
					return fmt.Errorf("%s: param %q has an unsupported schema type", where, p.Name)
				}
			}
		}
	}
	for _, match := range placeholders {
		if _, ok := declared[match[1]]; !ok {
			return fmt.Errorf("%s: path placeholder {%s} has no param", where, match[1])
		}
		if declared[match[1]].In != ParamInPath {
			return fmt.Errorf("%s: path placeholder {%s} must bind a param with in=path", where, match[1])
		}
	}
	for name := range pathParams {
		if !strings.Contains(op.Path, "{"+name+"}") {
			return fmt.Errorf("%s: param %q is bound to the path but the path has no placeholder", where, name)
		}
	}
	if bodyParams > 1 {
		return fmt.Errorf("%s: only one in=body param is allowed", where)
	}
	if bodyParams == 1 && !bodyMethods[op.Method] {
		return fmt.Errorf("%s: %s requests cannot carry a body param", where, op.Method)
	}
	if op.BodyParam != "" {
		p, ok := declared[op.BodyParam]
		if !ok {
			return fmt.Errorf("%s: body_param %q is not declared in params", where, op.BodyParam)
		}
		if p.In != ParamInBody {
			return fmt.Errorf("%s: body_param %q must use in=body", where, op.BodyParam)
		}
	}
	if len(op.InputSchema) > 0 {
		if raw, ok := op.InputSchema["type"]; ok {
			if name, ok := raw.(string); !ok || name != "object" {
				return fmt.Errorf("%s: input_schema must be an object schema", where)
			}
		}
	}
	return nil
}

func claimTool(owners map[string]string, owner, name string) error {
	key := strings.ToLower(name)
	if previous, ok := owners[key]; ok {
		return fmt.Errorf("tool %q declared by %s and %s", name, previous, owner)
	}
	owners[key] = owner
	return nil
}

func validatePathSyntax(path string) error {
	if strings.HasPrefix(path, "//") {
		return fmt.Errorf("path %q must not start with //", path)
	}
	if strings.ContainsAny(path, "?# \t\r\n") {
		return fmt.Errorf("path %q must not contain a query, fragment or whitespace", path)
	}
	if strings.Contains(path, "%") {
		return fmt.Errorf("path %q must not contain percent escapes", path)
	}
	for _, seg := range strings.Split(path, "/") {
		if seg == ".." || seg == "." {
			return fmt.Errorf("path %q must not contain . or .. segments", path)
		}
	}
	return nil
}

func validatePrefix(prefix string) error {
	if !strings.HasPrefix(prefix, "/") {
		return fmt.Errorf("prefix %q must start with /", prefix)
	}
	if err := validatePathSyntax(prefix); err != nil {
		return err
	}
	if strings.ContainsAny(prefix, "{}") {
		return fmt.Errorf("prefix %q must not contain placeholders", prefix)
	}
	return nil
}

func validateHeaders(where string, headers map[string]string) error {
	for name, value := range headers {
		if !headerNameRe.MatchString(name) {
			return fmt.Errorf("%s: %q is not a valid header name", where, name)
		}
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("%s: %q must not contain CR or LF", where, name)
		}
	}
	return nil
}

func validateAuth(where string, auth *Auth) error {
	switch auth.Scheme {
	case "", SchemeNone:
		return nil
	case SchemeBearer, SchemeHeader:
		if strings.TrimSpace(auth.TokenEnv) == "" {
			return fmt.Errorf("%s: token_env is required for scheme %q", where, auth.Scheme)
		}
		if !envNameRe.MatchString(auth.TokenEnv) {
			return fmt.Errorf("%s: token_env %q is not an environment variable name", where, auth.TokenEnv)
		}
		if auth.Scheme == SchemeHeader && !headerNameRe.MatchString(auth.HeaderName) {
			return fmt.Errorf("%s: header_name is required for scheme %q", where, auth.Scheme)
		}
		return nil
	default:
		return fmt.Errorf("%s: scheme %q must be none, bearer or header", where, auth.Scheme)
	}
}

func validateOrigin(origin string) error {
	if origin == "*" {
		return nil
	}
	parsed, err := url.Parse(origin)
	if err != nil {
		return fmt.Errorf("server.allowed_origins: %q: %w", origin, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("server.allowed_origins: %q must be http or https", origin)
	}
	if parsed.Host == "" {
		return fmt.Errorf("server.allowed_origins: %q must include a host", origin)
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return fmt.Errorf("server.allowed_origins: %q must be an origin without a path", origin)
	}
	return nil
}
