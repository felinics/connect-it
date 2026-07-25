package sessions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/memohai/connect-it/packages/core/connector"
)

const storedToolAllowlistVersion = 1

// StoredToolGrant is the immutable authorization snapshot persisted for one
// exposed tool name.
type StoredToolGrant struct {
	Name string             `json:"name"`
	Risk connector.ToolRisk `json:"risk"`
}

// StoredToolAllowlist is the only accepted tool_allowlist representation.
// Legacy bare arrays and unknown versions are intentionally rejected.
type StoredToolAllowlist struct {
	Version int               `json:"version"`
	Tools   []StoredToolGrant `json:"tools"`
}

// AllowlistInput is the wire shape of tool_allowlist. The three API states are
// encoded without any representable contradiction:
//   - Tools == nil, Null == false: omitted, expand current read tools
//   - Tools != nil (possibly empty): explicit list
//   - Null == true: explicit JSON null, always rejected
type AllowlistInput struct {
	Tools []string
	Null  bool
}

// UnmarshalJSON keeps an explicit [] distinct from an omitted field: encoding/json
// allocates a non-nil empty slice for [], and only null lands in the Null state.
func (a *AllowlistInput) UnmarshalJSON(data []byte) error {
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		a.Null = true
		return nil
	}
	return json.Unmarshal(data, &a.Tools)
}

func encodeStoredToolAllowlist(grants []StoredToolGrant) ([]byte, error) {
	if grants == nil {
		grants = []StoredToolGrant{}
	}
	sort.Slice(grants, func(i, j int) bool {
		return grants[i].Name < grants[j].Name
	})
	return json.Marshal(StoredToolAllowlist{
		Version: storedToolAllowlistVersion,
		Tools:   grants,
	})
}

func decodeStoredToolAllowlist(data []byte) (StoredToolAllowlist, error) {
	var envelope StoredToolAllowlist
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil {
		return StoredToolAllowlist{}, fmt.Errorf("decode grant envelope: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return StoredToolAllowlist{}, err
	}
	if envelope.Version != storedToolAllowlistVersion {
		return StoredToolAllowlist{}, fmt.Errorf("unsupported grant envelope version %d", envelope.Version)
	}
	if envelope.Tools == nil {
		return StoredToolAllowlist{}, errors.New("grant envelope tools must be an array")
	}

	seen := make(map[string]struct{}, len(envelope.Tools))
	for _, grant := range envelope.Tools {
		if _, _, ok := SplitExposedName(grant.Name); !ok {
			return StoredToolAllowlist{}, fmt.Errorf("invalid exposed tool name %q", grant.Name)
		}
		switch grant.Risk {
		case connector.RiskRead, connector.RiskWrite, connector.RiskDestructive:
		default:
			return StoredToolAllowlist{}, fmt.Errorf("invalid stored risk %q", grant.Risk)
		}
		if _, duplicate := seen[grant.Name]; duplicate {
			return StoredToolAllowlist{}, fmt.Errorf("duplicate grant %q", grant.Name)
		}
		seen[grant.Name] = struct{}{}
	}
	return envelope, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("grant envelope contains multiple JSON values")
	}
	return fmt.Errorf("decode trailing grant envelope data: %w", err)
}
