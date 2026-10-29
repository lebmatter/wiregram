package account

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"wiregram/internal/agent"
	"wiregram/internal/ai"
)

// engineFor reaches the models of provider p.
func engineFor(p agent.AgentProvider) (ai.Engine, error) {
	if p.Kind != agent.ProviderOpenAI {
		return nil, fmt.Errorf("provider %s is of an unknown kind %q", p.Name, p.Kind)
	}
	if err := agent.ValidateProviderURL(p.BaseURL); err != nil {
		return nil, err
	}
	var key string
	if p.APIKeyEnv != "" {
		key = strings.TrimSpace(os.Getenv(p.APIKeyEnv))
		if key == "" {
			return nil, fmt.Errorf("set %s to use provider %s", p.APIKeyEnv, p.Name)
		}
	}
	return &ai.OpenAI{BaseURL: p.BaseURL, Key: key, Timeout: agentTimeout}, nil
}

// providerError says how to fix a refused key in terms of p's settings.
func providerError(p agent.AgentProvider, err error) error {
	if !errors.Is(err, ai.ErrUnauthorized) {
		return err
	}
	if p.APIKeyEnv == "" {
		return fmt.Errorf("%s needs an API key; set its Key from in Settings > AI", p.Name)
	}
	return fmt.Errorf("%s rejected the API key in $%s; check it is a valid %s key",
		p.Name, p.APIKeyEnv, p.Name)
}
