package provider

import "go.uber.org/zap"

// NewWithClient builds an ApiProvider around an arbitrary SlackAPI
// implementation with both caches marked ready. It exists so handler and
// server tests can exercise tool handlers against a mock client.
func NewWithClient(client SlackAPI, logger *zap.Logger) *ApiProvider {
	ap := &ApiProvider{
		client: client,
		logger: logger,
	}
	ap.SkipCache()
	return ap
}
