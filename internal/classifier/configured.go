package classifier

import (
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/alexei-led/codex-router/internal/config"
)

// Configured returns the classifier that name selects among defs, ready to ask
// its endpoint, or nil and the reason it is off. The key and each {NAME}
// placeholder of the endpoint come from the env var of that name, read with
// getenv, else from the classifier's envFile. A value found in neither turns
// it off: the router still runs and pins still work. The reason never holds a
// value.
func Configured(name string, defs map[string]config.Classifier, getenv func(string) string) (Asker, string) {
	if name == config.ClassifierOff {
		return nil, "classifier is off in the config"
	}
	cc, ok := defs[name]
	if !ok {
		return nil, "no classifier configured"
	}
	var key string
	if cc.KeyEnv != "" {
		var why string
		if key, why = lookup(cc, cc.KeyEnv, getenv); why != "" {
			return nil, why
		}
	}
	names, err := config.EndpointPlaceholders(cc.Endpoint)
	if err != nil {
		return nil, "endpoint " + err.Error()
	}
	for _, field := range names {
		value, why := lookup(cc, field, getenv)
		if why != "" {
			return nil, why
		}
		cc.Endpoint = strings.ReplaceAll(cc.Endpoint, "{"+field+"}", url.PathEscape(value))
	}
	var a Adapter
	switch cc.API {
	case config.APISystemOne:
		a = SystemOne()
	case config.APIOpenAIDecisions:
		a = OpenAIDecisions()
	case config.APIOllama:
		a = Ollama()
	default:
		return nil, "unknown api " + cc.API
	}
	return New(cc, key, a, &http.Client{}, time.Now), ""
}

// lookup returns the value of name from the env, else from cc.EnvFile. The
// reason is empty on success and never holds the value.
func lookup(cc config.Classifier, name string, getenv func(string) string) (value, why string) {
	if value = getenv(name); value != "" {
		return value, ""
	}
	if cc.EnvFile == "" {
		return "", name + " is not set"
	}
	value, err := readEnvFile(cc.EnvFile, name, getenv)
	if err != nil {
		return "", name + " is not set; envFile " + err.Error()
	}
	return value, ""
}

// readEnvFile reads name from the dotenv file at path, with "~/" taken from HOME.
func readEnvFile(path, name string, getenv func(string) string) (string, error) {
	home := getenv("HOME")
	if home == "" {
		var err error
		if home, err = os.UserHomeDir(); err != nil {
			return "", err
		}
	}
	return config.ReadEnvKey(config.ExpandHome(path, home), name)
}
