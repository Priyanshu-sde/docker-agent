package environment

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/docker/docker-agent/pkg/path"
)

func ExpandAll(ctx context.Context, values []string, env Provider) ([]string, error) {
	var expandedEnv []string

	for _, value := range values {
		expanded, err := Expand(ctx, value, env)
		if err != nil {
			return nil, err
		}

		expandedEnv = append(expandedEnv, expanded)
	}

	return expandedEnv, nil
}

func Expand(ctx context.Context, value string, env Provider) (string, error) {
	var err error

	// Accept the JS-template `${env.VAR}` form as an alias for `${VAR}` so the
	// syntax used in prompts/headers also resolves here (issue #2615).
	value = path.NormalizeEnvRefs(value)

	expanded := os.Expand(value, func(name string) string {
		v, found := env.Get(ctx, name)
		if !found {
			err = fmt.Errorf("environment variable %q not set", name)
		}
		return v
	})
	if err != nil {
		return "", err
	}

	return expanded, nil
}

func ToValues(envMap map[string]string) []string {
	var values []string
	for k, v := range envMap {
		values = append(values, fmt.Sprintf("%s=%s", k, v))
	}
	slices.Sort(values)
	return values
}
