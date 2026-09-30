package api

import "testing"

// A route missing from routePolicy would otherwise ship with whatever the
// fallback is, so every registered pattern must be listed explicitly.
func TestRoutePolicy_CoversEveryRoute(t *testing.T) {
	routes := (&Handler{}).routes()
	for pattern := range routes {
		if _, ok := routePolicy[pattern]; !ok {
			t.Errorf("route %q has no routePolicy entry", pattern)
		}
	}
	for pattern := range routePolicy {
		if _, ok := routes[pattern]; !ok {
			t.Errorf("routePolicy entry %q matches no route", pattern)
		}
	}
}
