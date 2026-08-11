package outbox

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
)

// CanonicalJSON returns the one wire representation used by every outbox
// publisher. The envelope has fixed field order, UTC timestamps and a payload
// whose object keys are sorted by encoding/json. PostgreSQL remains the source
// of truth; this representation is only a deterministic delivery aid.
func CanonicalJSON(message Message) ([]byte, error) {
	if !validMessageID(message.EventID) || message.TenantID == "" || message.MerchantID == "" ||
		message.AggregateType == "" || message.AggregateID == "" || message.AggregateVersion < 1 ||
		message.Sequence < 1 || message.EventType == "" || message.SchemaVersion == "" ||
		message.OccurredAt.IsZero() || message.RecordedAt.IsZero() || len(message.Payload) == 0 {
		return nil, errors.New("outbox message is incomplete")
	}
	decoder := json.NewDecoder(bytes.NewReader(message.Payload))
	decoder.UseNumber()
	var payload any
	if err := decoder.Decode(&payload); err != nil {
		return nil, errors.New("outbox payload is not valid JSON")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return nil, err
	}
	payload, err := canonicalizeJSONValue(payload)
	if err != nil {
		return nil, err
	}
	canonicalPayload, err := json.Marshal(payload)
	if err != nil {
		return nil, errors.New("outbox payload cannot be canonicalized")
	}
	message.Payload = canonicalPayload
	message.OccurredAt = message.OccurredAt.UTC()
	message.RecordedAt = message.RecordedAt.UTC()
	return json.Marshal(message)
}

func validMessageID(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || strings.ContainsRune("-_.:", character) {
			continue
		}
		return false
	}
	return true
}

type canonicalNumber string

func (number canonicalNumber) MarshalJSON() ([]byte, error) { return []byte(number), nil }

func canonicalizeJSONValue(value any) (any, error) {
	switch typed := value.(type) {
	case json.Number:
		number, err := normalizeJSONNumber(string(typed))
		if err != nil {
			return nil, err
		}
		return canonicalNumber(number), nil
	case []any:
		for index, item := range typed {
			canonical, err := canonicalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			typed[index] = canonical
		}
	case map[string]any:
		for key, item := range typed {
			canonical, err := canonicalizeJSONValue(item)
			if err != nil {
				return nil, err
			}
			typed[key] = canonical
		}
	}
	return value, nil
}

func normalizeJSONNumber(raw string) (string, error) {
	negative := strings.HasPrefix(raw, "-")
	unsigned := strings.TrimPrefix(raw, "-")
	exponent := 0
	if position := strings.IndexAny(unsigned, "eE"); position >= 0 {
		parsed, err := strconv.Atoi(unsigned[position+1:])
		if err != nil || parsed < -10_000 || parsed > 10_000 {
			return "", errors.New("outbox payload number is outside the canonical range")
		}
		exponent = parsed
		unsigned = unsigned[:position]
	}
	integer, fraction := unsigned, ""
	if position := strings.IndexByte(unsigned, '.'); position >= 0 {
		integer, fraction = unsigned[:position], unsigned[position+1:]
	}
	digits := integer + fraction
	leading := len(digits) - len(strings.TrimLeft(digits, "0"))
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		return "0", nil
	}
	decimalPosition := len(integer) + exponent - leading
	var normalized string
	switch {
	case decimalPosition <= 0:
		normalized = "0." + strings.Repeat("0", -decimalPosition) + digits
	case decimalPosition >= len(digits):
		normalized = digits + strings.Repeat("0", decimalPosition-len(digits))
	default:
		normalized = digits[:decimalPosition] + "." + digits[decimalPosition:]
	}
	if strings.Contains(normalized, ".") {
		normalized = strings.TrimRight(normalized, "0")
		normalized = strings.TrimSuffix(normalized, ".")
	}
	if negative {
		normalized = "-" + normalized
	}
	return normalized, nil
}

// ParseCanonicalJSON rejects alternate encodings so reference consumers do
// not commit a body that differs from the publisher's stable representation.
func ParseCanonicalJSON(data []byte) (Message, error) {
	var message Message
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&message); err != nil {
		return Message{}, errors.New("event envelope is invalid")
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return Message{}, err
	}
	canonical, err := CanonicalJSON(message)
	if err != nil {
		return Message{}, err
	}
	if !bytes.Equal(canonical, data) {
		return Message{}, errors.New("event envelope is not canonical")
	}
	return message, nil
}

func ensureJSONEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("JSON value has trailing data")
	}
	return nil
}
