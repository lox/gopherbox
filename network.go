package gopherbox

import "strings"

// NetworkConfig configures optional curl network access.
type NetworkConfig struct {
	// AllowedURLPrefixes restrict curl to URLs starting with these prefixes.
	AllowedURLPrefixes []string

	// AllowedMethods restricts HTTP methods. Default: GET, HEAD.
	AllowedMethods []string
}

func (n *NetworkConfig) methodAllowed(method string) bool {
	if n == nil {
		return false
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	allowed := n.AllowedMethods
	if len(allowed) == 0 {
		allowed = []string{"GET", "HEAD"}
	}
	for _, m := range allowed {
		if strings.EqualFold(method, strings.TrimSpace(m)) {
			return true
		}
	}
	return false
}

func (n *NetworkConfig) urlAllowed(url string) bool {
	if n == nil {
		return false
	}
	if len(n.AllowedURLPrefixes) == 0 {
		return false
	}
	for _, prefix := range n.AllowedURLPrefixes {
		if strings.HasPrefix(url, prefix) {
			return true
		}
	}
	return false
}

// MethodAllowed reports whether an HTTP method is permitted by this network policy.
func (n *NetworkConfig) MethodAllowed(method string) bool {
	return n.methodAllowed(method)
}

// URLAllowed reports whether a URL is permitted by this network policy.
func (n *NetworkConfig) URLAllowed(url string) bool {
	return n.urlAllowed(url)
}
