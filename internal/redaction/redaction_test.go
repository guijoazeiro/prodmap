package redaction

import (
	"strings"
	"testing"
)

func TestValidatePublicValueDistinguishesCapabilitiesFromCredentials(t *testing.T) {
	accepted := []string{
		"token-service",
		"password-reset",
		"authorization-api",
		"bearer-worker",
		"secret-manager",
		"cookie-parser",
		"oauth-token-validator",
		"payment-authorization",
		"token validation is unavailable",
		"password reset service",
		"authorization dependency observed",
		"bearer authentication handler",
		"secret manager dependency",
		"cookie parser service",
	}
	for _, value := range accepted {
		t.Run(value, func(t *testing.T) {
			if err := ValidatePublicValue(Identifier, value); err != nil {
				t.Fatalf("ValidatePublicValue(%q) error = %v", value, err)
			}
		})
	}

	rejected := []string{
		"Authorization: Bearer fake-sensitive-value",
		"Authorization: Basic ZmFrZTpmYWtl",
		"Bearer fake-sensitive-value",
		"token=fake-sensitive-value",
		"access_token=fake-sensitive-value",
		"refresh_token=fake-sensitive-value",
		"password=fake-sensitive-value",
		"passwd=fake-sensitive-value",
		"api_key=fake-sensitive-value",
		"client_secret=fake-sensitive-value",
		`{"token":"fake-sensitive-value"}`,
		"postgres://user:fake-password@database/example",
		"mysql://user:fake-password@database/example",
		"https://user:fake-password@example.invalid/path",
		"-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----",
		"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiJmaWN0aW9uYWwifQ.fake-signature-value",
		"/tmp/prodmap-sensitive",
		`C:\\prodmap\\sensitive`,
		"value\x00with-nul",
		"value\nwith-newline",
	}
	for _, value := range rejected {
		t.Run("rejected", func(t *testing.T) {
			err := ValidatePublicValue(FreeText, value)
			if err == nil {
				t.Fatalf("ValidatePublicValue(%q) succeeded", value)
			}
			if strings.Contains(err.Error(), value) || strings.Contains(err.Error(), "fake-sensitive-value") {
				t.Fatalf("error leaked rejected content: %v", err)
			}
		})
	}
}

func TestProhibitedKeyUnderstandsNamingStylesWithoutSubstringFalsePositives(t *testing.T) {
	for _, key := range []string{
		"token", "access_token", "refresh-token", "idToken", "ApiKey", "privateKey", "ClientSecret",
		"password", "Passwd", "authorization", "proxyAuthorization", "cookie", "setCookie", "dsn",
		"body", "requestBody", "response_body", "payload", "raw_payload", "RawMetadata",
		"external_id", "gitHead", "VCSRevision", "imageReference", "artifactIdentity",
	} {
		if !ProhibitedKey(key) {
			t.Errorf("ProhibitedKey(%q) = false", key)
		}
	}
	for _, key := range []string{
		"tokenizer", "passwordless", "authorization_api", "bearer-worker", "secret_manager",
		"cookie_parser", "service_token_count", "rawness", "payload_size", "external_identity",
	} {
		if ProhibitedKey(key) {
			t.Errorf("ProhibitedKey(%q) = true", key)
		}
	}
}
