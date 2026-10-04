package sessions

import (
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/joho/godotenv"
)

// ErrSecretNotRoundTrippable refuses a secret value the dotenv format would
// not give back byte for byte: godotenv cannot hold a value ending in a
// backslash or ending with a double quote, and writes an integer
// unquoted, losing a leading zero. The variable is named; the value never is.
var ErrSecretNotRoundTrippable = errors.New("sessions: a secret value cannot be written to the secrets file exactly")

// EncodeSecrets renders a run's secrets as one dotenv file (godotenv). Each
// value is proven by decoding it back: a value that would come back different
// is refused (ErrSecretNotRoundTrippable) rather than written corrupt. Every
// $ is escaped, so reading expands nothing (DecodeSecrets).
func EncodeSecrets(values map[string]string) ([]byte, error) {
	s, err := godotenv.Marshal(values)
	if err != nil {
		return nil, err
	}
	back, err := godotenv.Unmarshal(s)
	var bad []string
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if got, ok := back[k]; err != nil || !ok || got != values[k] {
			bad = append(bad, k)
		}
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("%w: %v", ErrSecretNotRoundTrippable, bad)
	}
	return []byte(s + "\n"), nil
}

// DecodeSecrets reads a secrets file EncodeSecrets wrote.
func DecodeSecrets(b []byte) (map[string]string, error) {
	return godotenv.UnmarshalBytes(b)
}
