package legacycompat

import (
	"bytes"
	"crypto/md5" // #nosec G501 -- required only for the sunset legacy wire contract.
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const MaxLegacyBodyBytes = 64 << 10

var jsonMD5Fields = fieldSet("pid", "order_id", "currency", "token", "network", "amount", "notify_url", "redirect_url", "name", "payment_type", "signature")
var formMD5Fields = fieldSet("pid", "money", "out_trade_no", "notify_url", "return_url", "name", "type", "token", "network", "currency", "sign", "sign_type")

func fieldSet(fields ...string) map[string]bool {
	out := make(map[string]bool, len(fields))
	for _, field := range fields {
		out[field] = true
	}
	return out
}

func ParseJSONMD5(contentType string, raw []byte) (CreateRequest, error) {
	var values map[string]string
	var err error
	switch strings.TrimSpace(strings.Split(contentType, ";")[0]) {
	case "application/json":
		values, err = parseJSONObject(raw)
	case "application/x-www-form-urlencoded":
		values, err = parseEncoded(raw)
	default:
		return CreateRequest{}, fmt.Errorf("%w: unsupported content type", ErrInvalid)
	}
	if err != nil {
		return CreateRequest{}, err
	}
	if err = exactFields(values, jsonMD5Fields); err != nil {
		return CreateRequest{}, err
	}
	return buildRequest(ProtocolJSONMD5, values)
}

func ParseFormMD5(query string, contentType string, raw []byte) (CreateRequest, error) {
	queryValues, err := parseEncoded([]byte(query))
	if err != nil {
		return CreateRequest{}, err
	}
	values := queryValues
	if len(raw) > 0 {
		if strings.TrimSpace(strings.Split(contentType, ";")[0]) != "application/x-www-form-urlencoded" {
			return CreateRequest{}, fmt.Errorf("%w: unsupported content type", ErrInvalid)
		}
		form, err := parseEncoded(raw)
		if err != nil {
			return CreateRequest{}, err
		}
		for key, value := range form {
			if _, exists := values[key]; exists {
				return CreateRequest{}, fmt.Errorf("%w: query/body collision", ErrInvalid)
			}
			values[key] = value
		}
	}
	if err = exactFields(values, formMD5Fields); err != nil {
		return CreateRequest{}, err
	}
	return buildRequest(ProtocolFormMD5, values)
}

func buildRequest(protocol Protocol, values map[string]string) (CreateRequest, error) {
	signatureKey := "signature"
	amountKey, orderKey := "amount", "order_id"
	if protocol == ProtocolFormMD5 {
		signatureKey, amountKey, orderKey = "sign", "money", "out_trade_no"
		if signType := values["sign_type"]; signType != "" && signType != "MD5" {
			return CreateRequest{}, fmt.Errorf("%w: sign_type", ErrInvalid)
		}
	}
	for _, key := range []string{"pid", orderKey, amountKey, "notify_url", signatureKey} {
		if values[key] == "" {
			return CreateRequest{}, fmt.Errorf("%w: missing %s", ErrInvalid, key)
		}
	}
	if len(values[orderKey]) > 128 || len(values["pid"]) > 128 || len(values["name"]) > 2048 {
		return CreateRequest{}, ErrInvalid
	}
	canonical, err := Canonical(values, protocol)
	if err != nil {
		return CreateRequest{}, err
	}
	digest := sha256.Sum256([]byte(canonical))
	request := CreateRequest{Protocol: protocol, PID: values["pid"], OrderID: values[orderKey], Amount: values[amountKey], Currency: strings.ToUpper(values["currency"]), Token: strings.ToUpper(values["token"]), Network: strings.ToLower(values["network"]), PaymentType: values["type"], Name: values["name"], NotifyURL: values["notify_url"], ReturnURL: values["redirect_url"], Signature: values[signatureKey], Canonical: canonical, CanonicalHash: digest}
	if protocol == ProtocolFormMD5 {
		request.ReturnURL = values["return_url"]
	}
	if !utf8.ValidString(request.OrderID + request.Name + request.NotifyURL + request.ReturnURL) {
		return CreateRequest{}, ErrInvalid
	}
	return request, nil
}

func Canonical(values map[string]string, protocol Protocol) (string, error) {
	keys := make([]string, 0, len(values))
	for key, value := range values {
		if value == "" || protocol == ProtocolJSONMD5 && key == "signature" || protocol == ProtocolFormMD5 && (key == "sign" || key == "sign_type") {
			continue
		}
		if strings.ContainsAny(key+value, "\x00\r\n") {
			return "", ErrInvalid
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values[key])
	}
	return strings.Join(parts, "&"), nil
}

func VerifyMD5(canonical, received string, secret []byte) bool {
	if len(received) != md5.Size*2 || received != strings.ToLower(received) {
		return false
	}
	decoded := make([]byte, md5.Size)
	if _, err := hex.Decode(decoded, []byte(received)); err != nil {
		return false
	}
	h := md5.New() // #nosec G401 -- isolated compatibility verifier with enforced sunset.
	_, _ = io.WriteString(h, canonical)
	_, _ = h.Write(secret)
	return subtle.ConstantTimeCompare(decoded, h.Sum(nil)) == 1
}

func SignMD5(canonical string, secret []byte) string {
	h := md5.New() // #nosec G401 -- isolated compatibility signer with enforced sunset.
	_, _ = io.WriteString(h, canonical)
	_, _ = h.Write(secret)
	return hex.EncodeToString(h.Sum(nil))
}

func parseJSONObject(raw []byte) (map[string]string, error) {
	if len(raw) == 0 || len(raw) > MaxLegacyBodyBytes || !utf8.Valid(raw) {
		return nil, ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInvalid
	}
	values := map[string]string{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return nil, ErrInvalid
		}
		key, ok := keyToken.(string)
		if !ok || key == "" {
			return nil, ErrInvalid
		}
		if _, duplicate := values[key]; duplicate {
			return nil, fmt.Errorf("%w: duplicate field", ErrInvalid)
		}
		var scalar any
		if err = decoder.Decode(&scalar); err != nil {
			return nil, ErrInvalid
		}
		switch value := scalar.(type) {
		case string:
			values[key] = value
		case json.Number:
			f, err := strconv.ParseFloat(value.String(), 64)
			if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
				return nil, ErrInvalid
			}
			canonical := strconv.FormatFloat(f, 'f', -1, 64)
			if value.String() != canonical {
				return nil, fmt.Errorf("%w: noncanonical JSON number", ErrInvalid)
			}
			values[key] = canonical
		default:
			return nil, fmt.Errorf("%w: scalar strings/numbers only", ErrInvalid)
		}
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrInvalid
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, ErrInvalid
	}
	return values, nil
}

func parseEncoded(raw []byte) (map[string]string, error) {
	if len(raw) > MaxLegacyBodyBytes || !utf8.Valid(raw) {
		return nil, ErrInvalid
	}
	if len(raw) == 0 {
		return map[string]string{}, nil
	}
	if err := validateEncoded(string(raw)); err != nil {
		return nil, err
	}
	parsed, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, ErrInvalid
	}
	values := make(map[string]string, len(parsed))
	for key, list := range parsed {
		if key == "" || len(list) != 1 {
			return nil, fmt.Errorf("%w: duplicate field", ErrInvalid)
		}
		values[key] = list[0]
	}
	return values, nil
}

func validateEncoded(raw string) error {
	if strings.ContainsAny(raw, "\x00\r\n#") {
		return ErrInvalid
	}
	for index := 0; index < len(raw); index++ {
		if raw[index] != '%' {
			continue
		}
		if index+2 >= len(raw) || !isUpperHex(raw[index+1]) || !isUpperHex(raw[index+2]) {
			return fmt.Errorf("%w: malformed percent encoding", ErrInvalid)
		}
		decoded, _ := strconv.ParseUint(raw[index+1:index+3], 16, 8)
		if isUnreserved(byte(decoded)) {
			return fmt.Errorf("%w: overencoded unreserved byte", ErrInvalid)
		}
		index += 2
	}
	return nil
}

func isUpperHex(value byte) bool { return value >= '0' && value <= '9' || value >= 'A' && value <= 'F' }
func isUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || strings.ContainsRune("-._~", rune(value))
}

func exactFields(values map[string]string, allowed map[string]bool) error {
	for key := range values {
		if !allowed[key] {
			return fmt.Errorf("%w: unknown field %s", ErrInvalid, key)
		}
	}
	return nil
}
