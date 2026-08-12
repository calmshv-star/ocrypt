#!/usr/bin/env bash
set -euo pipefail

validation_mode="${1:-full}"
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
chart_path="${repository_root}/deploy/helm/merchant-platform"

if [[ "$validation_mode" != static && "$validation_mode" != full ]]; then
  echo "usage: deploy/validate.sh [static|full]" >&2
  exit 2
fi
for command_name in ruby jq rg; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 1; }
done

cd "$repository_root"
ruby <<'RUBY'
require "yaml"
require "json"
require "date"
files = Dir["infra/*.{yaml,yml}"] + Dir["deploy/observability/*.{yaml,yml}"] +
  Dir["deploy/helm/merchant-platform/*.{yaml,yml}"]
load_yaml = ->(path) do
  contents = File.read(path)
  if YAML.respond_to?(:safe_load_file)
    YAML.safe_load_file(path, permitted_classes: [Date, Time], aliases: true)
  else
    YAML.safe_load(contents, permitted_classes: [Date, Time], aliases: true, filename: path)
  end
end
files.each { |file| load_yaml.call(file) }
contracts = JSON.parse(File.read("deploy/runtime-contracts.json"))
expected = contracts.keys
tool_commands = %w[migration-control migration-control-worker migration-traffic-actuator]
local_commands = %w[bootstrap-envelope]
compose = load_yaml.call("infra/compose.yaml")
services = compose.fetch("services")
missing = expected + %w[migration migration-control-worker migration-traffic-actuator gateway] - services.keys
abort("Compose is missing workloads: #{missing.join(', ')}") unless missing.empty?
exposed = services.select { |_name, service| service.key?("ports") }
abort("base Compose file publishes host ports: #{exposed.keys.join(', ')}") unless exposed.empty?
abort("scanner/provider-health/rate/financial must be opt-in") unless services.dig("scanner", "profiles") == ["scanner"] && services.dig("provider-health-worker", "profiles") == ["provider-operations"] && services.dig("rate-worker", "profiles") == ["rates"] && services.dig("financial-api", "profiles") == ["financial"]
scanner = services.fetch("scanner")
abort("production scanner must use platform runtime snapshot keys") if scanner.dig("environment", "SCANNER_PLATFORM_RUNTIME_JSON").to_s.empty? || scanner.dig("environment", "SCANNER_SECRET_DIR").to_s.empty?
legacy_scanner_keys = %w[SCANNER_CHAIN_ID SCANNER_GENESIS_HASH SCANNER_PROVIDER_URLS SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG]
abort("base Compose scanner contains development static config") unless (legacy_scanner_keys & scanner.fetch("environment").keys).empty?
scanner_dev = load_yaml.call("infra/compose.dev.yaml").dig("services", "scanner", "environment")
abort("scanner static fallback is not confined to explicit development override") unless scanner_dev&.fetch("ENVIRONMENT") == "development" && scanner_dev&.fetch("SCANNER_UNSAFE_DEVELOPMENT_STATIC_CONFIG") == "true" && (legacy_scanner_keys - scanner_dev.keys).empty?
provider_health = services.fetch("provider-health-worker")
abort("provider health worker must remain private and isolated") if provider_health.key?("ports") || provider_health.fetch("networks") != ["data", "edge"]
abort("provider health runtime contract drift") unless provider_health.dig("environment", "PROVIDER_HEALTH_ADDRESS") == ":9100" && provider_health.dig("environment", "PROVIDER_HEALTH_SECRET_DIR") == "/run/provider-health-secrets"
publisher = services.fetch("platform-outbox-publisher")
abort("platform outbox publisher must be opt-in and private") unless publisher.fetch("profiles") == ["platform-runtime"] && !publisher.key?("ports") && publisher.fetch("networks") == ["data", "edge"]
abort("platform outbox publisher health must bind to loopback") unless publisher.dig("environment", "PLATFORM_OUTBOX_HEALTH_ADDRESS") == "127.0.0.1:9098"
abort("platform outbox publisher destination path is not exact") unless publisher.dig("environment", "PLATFORM_OUTBOX_DESTINATION_URL").to_s.include?("PLATFORM_OUTBOX_DESTINATION_URL")
publisher_secret_targets = publisher.fetch("secrets").map { |item| item.fetch("target") }.sort
abort("platform outbox publisher requires separate mTLS and bearer files") unless publisher_secret_targets == %w[platform-outbox-destination-ca.pem platform-outbox-destination.crt platform-outbox-destination.key platform-outbox-destination.bearer].sort
merchant_outbox = services.fetch("outbox-worker")
nats = services.fetch("nats-jetstream")
abort("JetStream Compose profile must remain opt-in and isolated") unless nats.fetch("profiles") == ["jetstream"] && !nats.key?("ports") && nats.fetch("networks") == ["eventbus"] && compose.dig("networks", "eventbus", "internal") == true
abort("JetStream Compose server image is not exactly pinned") unless nats.fetch("image").to_s.include?("nats:2.14.3-alpine")
abort("JetStream Compose server does not fail closed on empty configuration") unless nats.fetch("command").join(" ").include?("test -s /run/secrets/nats-server.conf")
abort("outbox Compose profile may egress only to PostgreSQL and JetStream") unless merchant_outbox.fetch("profiles") == ["jetstream"] && merchant_outbox.fetch("networks") == ["data", "eventbus"] && !merchant_outbox.key?("ports")
abort("outbox Compose mode is not explicit JetStream") unless merchant_outbox.dig("environment", "OUTBOX_PUBLISHER") == "jetstream" && merchant_outbox.dig("environment", "OUTBOX_NATS_URLS") == "tls://nats-jetstream:4222"
outbox_secret_targets = merchant_outbox.fetch("secrets").map { |item| item.fetch("target") }.sort
abort("outbox Compose requires separate NATS CA, certificate, key and credentials files") unless outbox_secret_targets == %w[outbox-nats-ca.pem outbox-nats-client.crt outbox-nats-client.key outbox-nats-publisher.creds].sort
abort("outbox Compose contains an inline delivery secret") if merchant_outbox.fetch("environment").keys.any? { |key| key == "OUTBOX_PUBLISH_TOKEN" || key.end_with?("TOKEN") || key.end_with?("CREDS") }
management = services.fetch("management-api")
admin = services.fetch("admin-api")
%w[MANAGEMENT_TLS_CERT_FILE MANAGEMENT_TLS_KEY_FILE].each { |key| abort("management missing #{key}") if management.dig("environment", key).to_s.empty? }
abort("admin management bridge is not HTTPS") unless admin.dig("environment", "MANAGEMENT_INTERNAL_URL").to_s.include?("https://management-api:8445")
abort("admin does not pin management CA") if admin.dig("environment", "MANAGEMENT_INTERNAL_CA_FILE").to_s.empty?
abort("admin merchant settings bridge is not HTTPS") unless admin.dig("environment", "MERCHANT_SETTINGS_INTERNAL_URL").to_s.include?("https://merchant-settings-api:8447")
%w[MERCHANT_SETTINGS_ASSERTION_KEY_FILE MERCHANT_SETTINGS_INTERNAL_CA_FILE MERCHANT_SETTINGS_INTERNAL_CLIENT_CERT_FILE MERCHANT_SETTINGS_INTERNAL_CLIENT_KEY_FILE MERCHANT_SETTINGS_INTERNAL_SERVER_NAME].each do |key|
  abort("admin merchant settings bridge missing #{key}") if admin.dig("environment", key).to_s.empty?
end
abort("admin financial bridge is not HTTPS") unless admin.dig("environment", "FINANCIAL_INTERNAL_URL").to_s.include?("https://financial-api:8444")
%w[FINANCIAL_INTERNAL_ASSERTION_KEY_FILE FINANCIAL_INTERNAL_CA_FILE FINANCIAL_INTERNAL_CLIENT_CERT_FILE FINANCIAL_INTERNAL_CLIENT_KEY_FILE FINANCIAL_INTERNAL_SERVER_NAME].each do |key|
  abort("admin financial bridge missing #{key}") if admin.dig("environment", key).to_s.empty?
end
financial = services.fetch("financial-api")
abort("financial API must remain opt-in and private") unless financial.fetch("profiles") == ["financial"] && !financial.key?("ports") && financial.fetch("networks") == ["data"]
abort("financial API does not require a pinned client CA") if financial.dig("environment", "FINANCIAL_TLS_CLIENT_CA_FILE").to_s.empty?
%w[FINANCIAL_MONITOR_CLIENT_CERT_FILE FINANCIAL_MONITOR_CLIENT_KEY_FILE FINANCIAL_MONITOR_CA_FILE FINANCIAL_MONITOR_SERVER_NAME].each do |key|
  abort("financial mTLS health probe missing #{key}") if financial.dig("environment", key).to_s.empty?
end
blackbox = services.fetch("blackbox-exporter")
blackbox_targets = blackbox.fetch("secrets").map { |item| item.fetch("target") }
abort("blackbox financial probe requires a dedicated client certificate") unless %w[financial-ca.pem financial-monitor-client.crt financial-monitor-client.key].all? { |target| blackbox_targets.include?(target) }
blackbox_config = File.read("deploy/observability/blackbox.yaml")
abort("blackbox financial TLS module lacks pinned mTLS") unless blackbox_config.include?("cert_file: /run/secrets/financial-monitor-client.crt") && blackbox_config.include?("key_file: /run/secrets/financial-monitor-client.key") && blackbox_config.include?("server_name: financial-api") && blackbox_config.include?("min_version: TLS13") && blackbox_config.include?("max_version: TLS13")
settings = services.fetch("merchant-settings-api")
abort("merchant settings must be opt-in and private") unless settings.fetch("profiles") == ["merchant-settings"] && !settings.key?("ports") && settings.fetch("networks") == ["data"]
%w[MERCHANT_SETTINGS_ASSERTION_KEY_FILE MERCHANT_SETTINGS_TLS_CERT_FILE MERCHANT_SETTINGS_TLS_KEY_FILE MERCHANT_SETTINGS_CLIENT_CA_FILE].each do |key|
  abort("merchant settings missing #{key}") if settings.dig("environment", key).to_s.empty?
end
abort("merchant settings key ring or explicit email admission flag is missing") if settings.dig("environment", "MERCHANT_INVITE_TOKEN_KEY_RING_FILE").to_s.empty? || settings.dig("environment", "MERCHANT_SETTINGS_EMAIL_INVITES_ENABLED").to_s.empty?
delivery = services.fetch("merchant-invitation-delivery-worker")
abort("invitation delivery must be opt-in without a host/data port") unless delivery.fetch("profiles") == ["merchant-settings"] && !delivery.key?("ports")
abort("invitation delivery must use isolated notifier egress") unless delivery.fetch("networks") == ["data", "edge"]
legacy = services.fetch("legacy-gateway")
abort("legacy compatibility Compose workload must be opt-in and unpublished") unless legacy.fetch("profiles") == ["legacy-compat"] && !legacy.key?("ports")
abort("legacy compatibility Compose network scope drift") unless legacy.fetch("networks") == ["data", "edge"]
abort("legacy compatibility Compose mode must be explicit production") unless legacy.dig("environment", "APP_ENV") == "production" && legacy.dig("environment", "LEGACY_COMPAT_ENABLED") == "true"
abort("legacy compatibility listener/health contract drift") unless legacy.dig("environment", "LEGACY_HTTP_ADDRESS") == ":8082" && legacy.dig("environment", "LEGACY_HEALTH_ADDRESS") == ":9101"
legacy_secret_targets = legacy.fetch("secrets").map { |item| item.fetch("target") }.sort
abort("legacy core mTLS requires distinct CA/certificate/key files") unless legacy_secret_targets == %w[legacy-core-ca.pem legacy-core-client.crt legacy-core-client.key].sort
abort("legacy credentials must be a read-only external directory") unless legacy.fetch("volumes").one? { |mount| mount.include?("/run/secrets/legacy-credentials:ro") }
gateway = File.read("deploy/gateway/nginx.conf")
abort("gateway management upstream is not verified TLS") unless gateway.include?("proxy_pass https://management-api:8445") && gateway.include?("proxy_ssl_verify on") && gateway.include?("proxy_ssl_trusted_certificate")
abort("gateway exposes an internal control/financial/settings path") if gateway.match?(%r{location[^\n]*(platform|financial|management|merchant-settings)/})
capability_routes = %w[/v1/public/payment-links /v1/payment-links /v1/checkout-sessions]
capability_routes.each do |route|
  signature = "location ~ ^#{route}(?:/|$) {"
  block = gateway.match(%r{#{Regexp.escape(signature)}(?<body>.*?)^    \}}m)
  abort("gateway does not give #{route} a unique management owner") unless block && block[:body].include?("proxy_pass https://management-api:8445")
  abort("gateway does not rate limit #{route}") unless block[:body].include?("limit_req zone=capability_per_ip") && block[:body].include?("limit_conn capability_connections")
end
core_location = gateway.lines.find { |line| line.include?("location ~ ^/v1/(payment-intents") }.to_s
abort("gateway core API allowlist is missing") if core_location.empty?
abort("gateway core API regex captures a management capability") if core_location.match?(/payment-links|checkout-sessions/)
rate_location = gateway.match(%r{location ~ \^/v1/public/rates/.*?(?<body>.*?)^    \}}m)
abort("gateway does not expose the closed normalized rate catalog to core") unless rate_location && rate_location[:body].include?("proxy_pass http://api:8080")
abort("gateway does not rate limit the normalized rate catalog") unless rate_location[:body].include?("limit_req zone=capability_per_ip") && rate_location[:body].include?("limit_conn capability_connections")
abort("shared gateway must not silently expose the source-IP-sensitive legacy adapter") if gateway.match?(%r{/legacy/(json-md5|form-md5)|/pay/check-status|/pay/checkout-counter})
helm_ingress = File.read("deploy/helm/merchant-platform/templates/ingress.yaml")
capability_routes.each { |route| abort("Helm ingress misses management route #{route}") unless helm_ingress.include?("- path: #{route}\n") }
values = load_yaml.call("deploy/helm/merchant-platform/values.yaml")
abort("Helm core paths miss the normalized rate catalog") unless values.dig("ingress", "coreAPIPaths").include?("/v1/public/rates")
helm_inventory = values.fetch("workloads").keys
abort("Helm workload inventory differs from runtime contracts") unless helm_inventory.sort == expected.sort
abort("chart may not claim platform outbox publication") unless values.dig("platformOutbox", "claimExternalPublication") == false
helm_publisher = values.dig("workloads", "platform-outbox-publisher")
abort("platform outbox publisher must be disabled in closed defaults") unless helm_publisher.fetch("enabled") == false
abort("platform outbox publisher may not create a Service") unless helm_publisher.dig("service", "enabled") == false
abort("platform outbox publisher must use one service identity") unless helm_publisher.fetch("replicas") == 1 && helm_publisher.fetch("workerIDEnv").empty?
helm_outbox = values.dig("workloads", "outbox-worker")
abort("JetStream outbox Helm default must be disabled and Service-free") unless helm_outbox.fetch("enabled") == false && helm_outbox.dig("service", "enabled") == false
abort("JetStream outbox Helm default must have no admitted broker egress") unless helm_outbox.dig("network", "internalEgress").empty? && helm_outbox.dig("network", "httpsEgressPeers").empty?
helm_api = values.dig("workloads", "api")
helm_reports = values.dig("workloads", "reconciliation-worker")
abort("production report runtimes must use shared S3") unless helm_api.dig("env", "RECONCILIATION_OBJECT_STORE") == "s3" && helm_reports.dig("env", "RECONCILIATION_OBJECT_STORE") == "s3"
abort("reconciliation worker health must remain pod-private") unless helm_reports.dig("service", "enabled") == false
abort("invitation delivery health must remain pod-private") unless values.dig("workloads", "merchant-invitation-delivery-worker", "service", "enabled") == false
abort("invitation delivery must be disabled in closed default values") unless values.dig("workloads", "merchant-invitation-delivery-worker", "enabled") == false
abort("Helm ingress exposes merchant settings directly") if helm_ingress.include?("merchant-settings-api")
api_object_secrets = helm_api.fetch("secretFiles").map { |item| item.dig("secretRef", "name") if item.fetch("path").include?("s3-") }.compact.uniq
worker_object_secrets = helm_reports.fetch("secretFiles").map { |item| item.dig("secretRef", "name") if item.fetch("path").include?("s3-") }.compact.uniq
abort("API and reconciliation worker reuse object credentials") unless !api_object_secrets.empty? && !worker_object_secrets.empty? && (api_object_secrets & worker_object_secrets).empty?
abort("API must receive only public report signing keys") unless helm_api.dig("env", "RECONCILIATION_SIGNING_PUBLIC_KEYS").to_s.include?("/run/secrets/reconciliation-signing-public-key") && helm_api.fetch("secretFiles").none? { |item| item.fetch("path").include?("signing-key") || item.fetch("path").include?("signing-private") }
api_source = File.read("backend/cmd/api/main.go") + File.read("backend/internal/httpapi/server.go")
abort("authenticated reconciliation download deadline override is missing") unless api_source.include?("RECONCILIATION_DOWNLOAD_TIMEOUT_SECONDS") && api_source.include?("NewResponseController") && api_source.include?("SetWriteDeadline")
compose_api = services.fetch("api")
compose_reports = services.fetch("reconciliation-worker")
abort("Compose report runtimes must use shared S3") unless compose_api.dig("environment", "RECONCILIATION_OBJECT_STORE") == "s3" && compose_reports.dig("environment", "RECONCILIATION_OBJECT_STORE") == "s3"
reconciliation_contract = contracts.fetch("reconciliation-worker")
abort("reconciliation capability role drift") unless reconciliation_contract.fetch("databaseRole") == "merchant_reconciliation_worker"
retention = services.fetch("retention-worker")
abort("retention worker must be opt-in without a host/data Service") unless retention.fetch("profiles") == ["retention"] && !retention.key?("ports") && retention.fetch("networks") == ["data", "edge"]
abort("retention worker must fail closed to production S3") unless retention.dig("environment", "APP_ENV") == "production" && retention.dig("environment", "RETENTION_OBJECT_STORE") == "s3"
abort("retention worker health must remain private on :9099") unless retention.dig("environment", "RETENTION_HEALTH_ADDRESS") == ":9099"
retention_secret_targets = retention.fetch("secrets").map { |item| item.fetch("target") }.sort
expected_retention_secrets = %w[retention-signing-private-key retention-s3-access-key-id retention-s3-secret-access-key retention-s3-session-token].sort
abort("retention requires separate Ed25519 and S3 credential files") unless retention_secret_targets == expected_retention_secrets
helm_retention = values.dig("workloads", "retention-worker")
abort("retention Helm default must remain disabled and Service-free") unless helm_retention.fetch("enabled") == false && helm_retention.dig("service", "enabled") == false
abort("retention Helm health/identity contract drift") unless helm_retention.fetch("port") == 9099 && helm_retention.dig("env", "RETENTION_HEALTH_ADDRESS") == ":9099" && helm_retention.fetch("workerIDEnv") == "RETENTION_WORKER_ID"
abort("retention Helm runtime must require production S3") unless helm_retention.dig("env", "APP_ENV") == "production" && helm_retention.dig("env", "RETENTION_OBJECT_STORE") == "s3"
abort("retention Helm requires four mounted secret files") unless helm_retention.fetch("secretFiles").length == 4
retention_contract = contracts.fetch("retention-worker")
retention_source = File.read("backend/cmd/retention-worker/main.go")
retention_source_keys = retention_source.scan(/"(APP_ENV|RETENTION_[A-Z0-9_]+)"/).flatten.uniq.sort
abort("retention source/runtime contract env drift") unless retention_contract.fetch("requiredEnv").sort == retention_source_keys
abort("retention capability role drift") unless retention_contract.fetch("databaseRole") == "retention_archive_worker"
retention_control = services.fetch("retention-control-scheduler")
abort("retention control scheduler must accompany platform-admin and remain private/database-only") unless retention_control.fetch("profiles").sort == ["control", "retention"] && !retention_control.key?("ports") && retention_control.fetch("networks") == ["data"]
abort("retention control scheduler must use production loopback health") unless retention_control.dig("environment", "APP_ENV") == "production" && retention_control.dig("environment", "RETENTION_CONTROL_HEALTH_ADDRESS") == "127.0.0.1:9102"
retention_control_contract = contracts.fetch("retention-control-scheduler")
abort("retention control scheduler capability drift") unless retention_control_contract.fetch("databaseRole") == "retention_control_scheduler"
helm_platform_admin = values.dig("workloads", "platform-admin-api")
helm_retention_control = values.dig("workloads", "retention-control-scheduler")
abort("platform-admin Helm readiness requires retention-control-scheduler") if helm_platform_admin.fetch("enabled") && !helm_retention_control.fetch("enabled")
legacy_contract = contracts.fetch("legacy-gateway")
helm_legacy = values.dig("workloads", "legacy-gateway")
abort("legacy Helm default must remain disabled and Service-free") unless helm_legacy.fetch("enabled") == false && helm_legacy.dig("service", "enabled") == false
abort("legacy Helm closed defaults must admit no network peers") unless helm_legacy.dig("network", "ingressFrom").empty? && helm_legacy.dig("network", "internalEgress").empty? && helm_legacy.dig("network", "httpsEgressPeers").empty?
abort("legacy Helm listener/health identity drift") unless helm_legacy.fetch("port") == 9101 && helm_legacy.fetch("workerIDEnv") == "LEGACY_WORKER_ID" && helm_legacy.dig("env", "LEGACY_HTTP_ADDRESS") == ":8082" && helm_legacy.dig("env", "LEGACY_HEALTH_ADDRESS") == ":9101"
abort("legacy Helm requires core mTLS and external credential files") unless helm_legacy.fetch("secretFiles").length == 3 && helm_legacy.fetch("secretDirectories").length == 1
abort("legacy capability role drift") unless legacy_contract.fetch("databaseRole") == "legacy_compat_runtime"
migration_worker = services.fetch("migration-control-worker")
abort("migration verifier must be opt-in, one-shot and unpublished") unless migration_worker.fetch("profiles") == ["migration-control"] && migration_worker.fetch("restart") == "no" && !migration_worker.key?("ports")
abort("migration verifier may reach only PostgreSQL and explicit HTTPS providers") unless migration_worker.fetch("networks") == ["data", "edge"]
abort("migration verifier must default to non-mutating mode") unless migration_worker.dig("environment", "APP_ENV") == "production" && migration_worker.dig("environment", "MIGRATION_EXECUTE").to_s.include?("MIGRATION_EXECUTE:-false")
abort("migration verifier requires five external config/key/mTLS files") unless migration_worker.fetch("secrets").length == 5
helm_verifier = values.fetch("migrationVerifier")
abort("Helm migration verifier must default closed") unless helm_verifier.fetch("enabled") == false && helm_verifier.fetch("execute") == false
abort("Helm migration verifier requires five external files and no default egress") unless helm_verifier.fetch("secretFiles").length == 5 && helm_verifier.fetch("httpsEgressPeers").empty?
migration_actuator = services.fetch("migration-traffic-actuator")
abort("migration actuator must be opt-in, one-shot and unpublished") unless migration_actuator.fetch("profiles") == ["migration-control"] && migration_actuator.fetch("restart") == "no" && !migration_actuator.key?("ports")
abort("migration actuator network scope drift") unless migration_actuator.fetch("networks") == ["data", "edge"]
abort("migration actuator process DB env drift") unless migration_actuator.dig("environment", "MIGRATION_ACTUATOR_DATABASE_URL").to_s.include?("MIGRATION_TRAFFIC_ACTUATOR_DATABASE_URL")
abort("migration actuator requires five external mTLS/signature files") unless migration_actuator.fetch("secrets").length == 5
helm_actuator = values.fetch("migrationActuator")
abort("Helm migration actuator must default closed") unless helm_actuator.fetch("enabled") == false
abort("Helm migration actuator requires five external files and no default egress") unless helm_actuator.fetch("secretFiles").length == 5 && helm_actuator.fetch("httpsEgressPeers").empty?
actuator_source = File.read("backend/cmd/migration-traffic-actuator/config.go") + File.read("backend/cmd/migration-traffic-actuator/main.go")
abort("migration actuator source/runtime DB env drift") unless actuator_source.include?("MIGRATION_ACTUATOR_DATABASE_URL")
%w[tls.VersionTLS13 CheckRedirect VerifyActuatorAck].each { |evidence| abort("migration actuator transport/signature contract misses #{evidence}") unless actuator_source.include?(evidence) }
abort("migration actuator may not use an HTTP proxy") unless actuator_source.match?(/Proxy:\s*nil/)
migration_sql = File.read("backend/migrations/000021_shadow_migration_control.up.sql")
migration_down = File.read("backend/migrations/000021_shadow_migration_control.down.sql")
%w[migration_control_worker migration_traffic_actuator migration_apply_watch_address migration_apply_order migration_observe_transfer_identity migration_payment_credit_fence].each do |evidence|
  abort("000021 migration misses #{evidence}") unless migration_sql.include?(evidence)
end
abort("000021 down migration does not remove runtime fences") unless %w[migration_payment_credit_fence migration_provider_observation migration_transfer_observation migration_imported_address_never_release].all? { |name| migration_down.include?(name) }
outbox_contract = contracts.fetch("outbox-worker")
outbox_modes = outbox_contract.fetch("publisherModes")
abort("outbox publisher modes drift") unless outbox_modes.keys.sort == %w[https jetstream]
outbox_expected_keys = (outbox_contract.fetch("requiredEnv") + outbox_modes.values.flatten).uniq.select { |key| key == "APP_ENV" || key.start_with?("OUTBOX_") }.sort
outbox_source = Dir["backend/cmd/worker/*.go"].map { |path| File.read(path) }.join("\n")
outbox_source_keys = outbox_source.scan(/"(APP_ENV|OUTBOX_[A-Z0-9_]+)"/).flatten.uniq.sort
abort("outbox source/runtime mode contract env drift") unless outbox_source_keys == outbox_expected_keys
%w[OUTBOX_NATS_CA_FILE OUTBOX_NATS_SERVER_NAME OUTBOX_NATS_CLIENT_CERT_FILE OUTBOX_NATS_CLIENT_KEY_FILE OUTBOX_NATS_CREDS_FILE].each do |key|
  abort("JetStream Helm/Compose parity misses #{key}") unless helm_outbox.fetch("env").key?(key) && merchant_outbox.fetch("environment").key?(key)
end
abort("JetStream authentication must select credentials OR token file") if helm_outbox.fetch("env").key?("OUTBOX_NATS_TOKEN_FILE") || merchant_outbox.fetch("environment").key?("OUTBOX_NATS_TOKEN_FILE")
env_example = File.read("infra/.env.example")
abort("reconciliation example login does not name its exact capability") unless env_example.include?("RECONCILIATION_DATABASE_URL=postgres://merchant_reconciliation_worker_login:")
abort("retention example login does not name its exact capability") unless env_example.include?("RETENTION_DATABASE_URL=postgres://retention_archive_worker_login:")
abort("merchant settings examples do not name exact capabilities") unless env_example.include?("MERCHANT_SETTINGS_DATABASE_URL=postgres://merchant_settings_api_runtime_login:") && env_example.include?("MERCHANT_SESSION_REVOCATION_DATABASE_URL=postgres://merchant_session_revocation_worker_login:")
abort("invitation delivery example does not name its exact capability") unless env_example.include?("MERCHANT_INVITATION_DELIVERY_DATABASE_URL=postgres://merchant_invitation_delivery_worker_login:")
abort("provider health example does not name its exact capability") unless env_example.include?("PROVIDER_HEALTH_DATABASE_URL=postgres://merchant_provider_health_worker_login:")
abort("legacy example login does not name its exact runtime capability") unless env_example.include?("LEGACY_DATABASE_URL=postgres://legacy_compat_runtime_login:")
abort("legacy admission examples do not use distinct requester/approver logins") unless env_example.include?("LEGACY_REQUESTER_DATABASE_URL=postgres://legacy_compat_requester_login:") && env_example.include?("LEGACY_APPROVER_DATABASE_URL=postgres://legacy_compat_approver_login:")
abort("migration workload examples do not use exact isolated capabilities") unless env_example.include?("MIGRATION_CONTROL_DATABASE_URL=postgres://migration_control_worker_login:") && env_example.include?("MIGRATION_ACTUATOR_DATABASE_URL=postgres://migration_traffic_actuator_login:")
abort("retention control example does not use its scheduler capability") unless env_example.include?("RETENTION_CONTROL_DATABASE_URL=postgres://retention_control_scheduler_login:")
discovered_commands = Dir["backend/cmd/*/main.go"].map { |path| File.basename(File.dirname(path)) }.sort
declared_commands = contracts.values.map { |contract| contract.fetch("command") }.uniq.sort
known_commands = (declared_commands + tool_commands + local_commands).sort
abort("runtime/tool command inventory drift: discovered=#{discovered_commands.join(',')} declared=#{known_commands.join(',')}") unless discovered_commands == known_commands
dockerfile = File.read("deploy/docker/Dockerfile.backend")
missing_docker_commands = declared_commands.reject { |command| dockerfile.include?(command) }
abort("Dockerfile command allowlist drift: #{missing_docker_commands.join(',')}") unless missing_docker_commands.empty?
ci_images = load_yaml.call(".github/workflows/ci.yml").dig("jobs", "container-images", "strategy", "matrix", "binary").sort
expected_images = (declared_commands + tool_commands + ["migrations"]).sort
abort("CI image/SBOM/scan matrix differs from runtime commands") unless ci_images == expected_images
backend_internal_source = Dir["backend/internal/**/*.go"].map { |path| File.read(path) }.join("\n")
helm_database_secrets = {}
compose_database_sources = {}
contracts.each do |name, contract|
  helm = values.fetch("workloads").fetch(name)
  helm_env = helm.fetch("env").keys + helm.fetch("secretEnv").map { |item| item.fetch("name") }
  worker_id = helm.fetch("workerIDEnv")
  helm_env << worker_id unless worker_id.empty?
  compose_env = services.fetch(name).fetch("environment").keys
  missing_helm = contract.fetch("requiredEnv") - helm_env
  missing_compose = contract.fetch("requiredEnv") - compose_env
  abort("Helm #{name} misses env: #{missing_helm.join(', ')}") unless missing_helm.empty?
  abort("Compose #{name} misses env: #{missing_compose.join(', ')}") unless missing_compose.empty?
  abort("Helm #{name} port/scheme drift") unless helm.fetch("port") == contract.fetch("port") && helm.fetch("probeScheme") == contract.fetch("scheme")
  command_source = Dir["backend/cmd/#{contract.fetch('command')}/**/*.go"].map { |path| File.read(path) }.join("\n")
  absent_source_keys = contract.fetch("requiredEnv").reject do |key|
    declared = command_source.include?(%Q{"#{key}"}) || backend_internal_source.include?(%Q{"#{key}"})
    if !declared && name == "financial-worker" && (match = key.match(/\AFINANCIAL_(BUILDER|SIGNER|BROADCASTER|FINALITY|EVENT_SINK)_(URL|TOKEN_FILE)\z/))
      declared = (command_source + backend_internal_source).include?(%Q{remoteConfig("#{match[1]}")})
    end
    declared
  end
  abort("runtime source no longer declares #{name} env: #{absent_source_keys.join(', ')}") unless absent_source_keys.empty?
  database_env = contract.fetch("requiredEnv").find { |key| key.end_with?("DATABASE_URL") }
  abort("runtime contract #{name} has no database env") unless database_env
  source = contract.fetch("composeDatabaseVariable")
  compose_value = services.fetch(name).fetch("environment").fetch(database_env).to_s
  abort("Compose #{name} does not use isolated #{source}") unless compose_value.include?("${#{source}")
  abort("Compose database source reused: #{source}") if compose_database_sources.key?(source)
  compose_database_sources[source] = name
  secret_item = helm.fetch("secretEnv").find { |item| item.fetch("name") == database_env }
  abort("Helm #{name} has no database Secret ref") unless secret_item
  secret_name = secret_item.fetch("secretRef").fetch("name")
  abort("Helm database Secret reused by #{helm_database_secrets[secret_name]} and #{name}") if helm_database_secrets.key?(secret_name)
  helm_database_secrets[secret_name] = name
  grants = File.read("deploy/postgres/runtime-grants.sql")
  bootstrap = File.read("deploy/postgres/bootstrap-roles.sql")
  role = contract.fetch("databaseRole")
  abort("database role #{role} is not bootstrapped/granted") unless bootstrap.include?(role) && grants.include?(role)
end
probe_source = File.read("deploy/docker/probe.go")
probe_template = File.read("deploy/helm/merchant-platform/templates/workloads.yaml")
abort("loopback publisher probe must disable proxies and redirects") unless probe_source.match?(/Proxy:\s*nil/) && probe_source.include?("CheckRedirect") && probe_source.include?("IsLoopback")
abort("Helm publisher must use the loopback exec readiness probe") unless probe_template.include?(%q{eq $name "platform-outbox-publisher"}) && probe_template.include?("command: [/app/probe, /readyz]")
grants = File.read("deploy/postgres/runtime-grants.sql")
%w[platform_route_runtime_admission platform_wallet_runtime_admission sandbox_test_credential_admitted sandbox_workspaces sandbox_idempotency scanner_runtime_config_evidence list_current_admin_merchant_memberships platform_outbox_publisher].each do |admission|
  abort("runtime grants omit #{admission}") unless grants.include?(admission)
end
retention_runtime_functions = %w[retention_claim_archive_batch retention_acknowledge_archive retention_fail_archive retention_claim_prune retention_advance_prune retention_worker_health]
retention_control_functions = %w[create_retention_policy_version create_retention_legal_hold release_retention_legal_hold]
retention_runtime_functions.each { |name| abort("retention runtime grants omit #{name}") unless grants.include?(name) }
retention_control_functions.each { |name| abort("retention control function is not explicitly revoked") unless grants.match?(/REVOKE EXECUTE ON FUNCTION.*?#{name}/m) }

iam = JSON.parse(File.read("infra/retention/worker-iam-policy.example.json"))
allowed_actions = iam.fetch("Statement").select { |statement| statement.fetch("Effect") == "Allow" }.flat_map { |statement| statement.fetch("Action") }.sort
denied_actions = iam.fetch("Statement").select { |statement| statement.fetch("Effect") == "Deny" }.flat_map { |statement| statement.fetch("Action") }.sort
abort("retention IAM allowlist drift") unless allowed_actions == %w[s3:GetBucketVersioning s3:GetObject s3:GetObjectLockConfiguration s3:PutObject].sort
abort("retention IAM must deny Delete/List") unless denied_actions == %w[s3:DeleteObject s3:DeleteObjectVersion s3:ListBucket].sort
versioning = JSON.parse(File.read("infra/retention/bucket-versioning.example.json"))
object_lock = JSON.parse(File.read("infra/retention/bucket-object-lock.example.json"))
abort("retention bucket versioning example is not enabled") unless versioning.fetch("Status") == "Enabled"
abort("retention bucket lock example is not COMPLIANCE") unless object_lock.fetch("ObjectLockEnabled") == "Enabled" && object_lock.dig("Rule", "DefaultRetention", "Mode") == "COMPLIANCE"
stream = JSON.parse(File.read("deploy/nats/merchant-events-stream.json"))
abort("JetStream stream name/subject drift") unless stream.fetch("name") == "MERCHANT_EVENTS_V1" && stream.fetch("subjects") == ["merchant.events.v1"] && stream.fetch("subjects").none? { |subject| subject.match?(/tenant|merchant[_-]?id/i) }
abort("JetStream production retention policy drift") unless stream.fetch("retention") == "limits" && stream.fetch("discard") == "old" && stream.fetch("storage") == "file" && stream.fetch("num_replicas") >= 3 && stream.fetch("max_age") > 0 && stream.fetch("max_bytes") > 0 && stream.fetch("max_msgs") > 0 && stream.fetch("max_msg_size") == 1_048_576
abort("JetStream deletion controls are not denied") unless stream.fetch("deny_delete") && stream.fetch("deny_purge") && !stream.fetch("allow_rollup_hdrs")
abort("JetStream duplicate window is below publisher retry cap") unless stream.fetch("duplicate_window") >= 15 * 60 * 1_000_000_000
consumer = JSON.parse(File.read("deploy/nats/merchant-events-reference-consumer.json"))
abort("reference consumer is not fixed durable explicit-ack pull recovery") unless consumer.fetch("name") == "MERCHANT_EVENTS_REFERENCE_V1" && consumer.fetch("durable_name") == consumer.fetch("name") && consumer.fetch("filter_subject") == "merchant.events.v1" && consumer.fetch("ack_policy") == "explicit" && !consumer.key?("deliver_subject")
publisher_permissions = JSON.parse(File.read("deploy/nats/publisher-permissions.example.json"))
denied_nats = publisher_permissions.dig("publish", "deny")
abort("publisher role can delete or purge JetStream data") unless %w[$JS.API.STREAM.DELETE.> $JS.API.STREAM.PURGE.> $JS.API.STREAM.MSG.DELETE.>].all? { |subject| denied_nats.include?(subject) }
eventbus_source = Dir["backend/internal/eventbus/*.go"].map { |path| File.read(path) }.join("\n")
abort("JetStream publisher does not bind stable message ID and expected stream") unless eventbus_source.include?("jetstream.WithMsgID(message.EventID)") && eventbus_source.include?("jetstream.WithExpectStream(StreamName)")
abort("JetStream publisher lacks canonical envelope size admission") unless eventbus_source.include?("MaxMessageBytes") && eventbus_source.include?("ack.Sequence == 0")
abort("JetStream publisher does not pin TLS 1.3 exactly") unless eventbus_source.include?("MinVersion:   tls.VersionTLS13") && eventbus_source.include?("MaxVersion:   tls.VersionTLS13")
abort("JetStream reference consumer does not commit before confirmed ack") unless eventbus_source.index("c.inbox.Commit") < eventbus_source.index("msg.DoubleAck")
legacy_source = Dir["backend/internal/legacycompat/*.go"].map { |path| File.read(path) }.join("\n")
webhook_source = Dir["backend/internal/webhook/*.go"].map { |path| File.read(path) }.join("\n")
md5_imports = Dir["backend/**/*.go"].select { |path| File.read(path).match?(/"crypto\/md5"/) }
abort("legacy MD5 is not confined to the compatibility canonicalizer") unless md5_imports == ["backend/internal/legacycompat/canonical.go"]
abort("legacy callback acknowledgement is not exact lowercase") unless legacy_source.include?('ack == "ok" || ack == "success"') && !legacy_source.include?("strings.ToLower(strings.TrimSpace(body))")
abort("legacy HTTP boundary does not explicitly reject forwarding headers") unless legacy_source.include?("hasForwardingHeaders") && %w[Forwarded X-Forwarded-For X-Real-IP].all? { |header| legacy_source.include?(header) }
abort("legacy capability status is not independently bounded by peer and trade") unless legacy_source.include?(%q{"ip\x1f"+peer.String()}) && legacy_source.include?(%q{"trade\x1f"+tradeID})
abort("legacy callback transport does not pin HTTPS:443/TLS1.3/no proxy/no redirects") unless webhook_source.include?(%q{port != "443"}) && legacy_source.include?("MinVersion: tls.VersionTLS13") && legacy_source.include?("MaxVersion: tls.VersionTLS13") && legacy_source.match?(/Proxy:\s*nil/) && legacy_source.include?("legacy callback redirects forbidden")
legacy_command_source = Dir["backend/cmd/legacy-gateway/*.go"].map { |path| File.read(path) }.join("\n")
abort("legacy readyz does not verify 000018 grants and external credential files") unless legacy_source.include?("func (service Service) Ready") && legacy_source.include?("service.Repository.Ready") && legacy_source.include?("service.Secrets.Read") && legacy_command_source.include?("service.Ready(readyCtx)")
legacy_http = File.read("backend/internal/legacycompat/http.go")
legacy_openapi = load_yaml.call("contracts/legacy-openapi.yaml")
source_paths = legacy_http.scan(/mux\.HandleFunc\("[A-Z]+ ([^"]+)"/).flatten.uniq.sort
contract_paths = legacy_openapi.fetch("paths").keys.sort
abort("legacy source/OpenAPI path drift: source=#{source_paths.join(',')} contract=#{contract_paths.join(',')}") unless source_paths == contract_paths
legacy_migration = File.read("backend/migrations/000018_legacy_compatibility.up.sql")
%w[legacy_compat_runtime legacy_compat_admission_requester legacy_compat_admission_approver clock_timestamp schema_migrations has_table_privilege].each do |evidence|
  abort("legacy migration misses #{evidence}") unless legacy_migration.include?(evidence)
end
abort("legacy cursor can skip a missing event") unless legacy_migration.include?("requested_sequence<>current_sequence+1")
abort("legacy callback bytes/key version are not frozen before cursor advance") unless legacy_migration.include?("frozen_body=requested_body") && legacy_migration.include?("credential_version_id=requested_credential") && legacy_migration.include?("callback_key_id=requested_key_id")
abort("legacy callback fence can commit after lease expiry") unless legacy_migration.scan("lease_until>authoritative_at").length >= 2
abort("legacy admission is not a DB-clock 30-minute two-person boundary") unless legacy_migration.include?("interval '30 minutes'") && legacy_migration.include?("request_row.requested_by=session_user")
legacy_grants = File.read("deploy/postgres/runtime-grants.sql")
abort("legacy admission roles may execute something other than the JSON manifest importers") unless legacy_grants.include?("request_legacy_compat_config_admission(uuid,jsonb)") && legacy_grants.include?("approve_legacy_compat_config_admission(uuid,jsonb)") && !legacy_grants.match?(/GRANT EXECUTE ON FUNCTION\s+request_legacy_compat_config_admission\(uuid,uuid/m)
legacy_docs = %w[en ru de fr es zh-CN].map { |locale| "docs/#{locale}/legacy-migration.md" }
abort("six localized legacy migration guides are incomplete") unless legacy_docs.all? { |path| File.exist?(path) && File.read(path).include?("000018") }
migration_docs = %w[en ru de fr es zh-CN].map { |locale| "docs/#{locale}/shadow-migration.md" }
abort("six localized shadow migration guides are incomplete") unless migration_docs.all? { |path| File.exist?(path) && File.read(path).include?("000021") }
abort("legacy golden examples are incomplete") unless %w[json-md5-create.form json-md5-settled-callback.json form-md5-create.query].all? { |name| File.exist?("examples/legacy-compat/#{name}") }
release_manifest = JSON.parse(File.read("deploy/release/manifest.example.json"))
legacy_release = release_manifest.fetch("legacy_compatibility")
abort("legacy release evidence must default closed and name migration/runtime role") unless legacy_release.fetch("enabled") == false && legacy_release.fetch("migration").start_with?("000018_") && legacy_release.fetch("database_role") == "legacy_compat_runtime" && legacy_release.fetch("service_exposed") == false && legacy_release.fetch("shared_ingress_exposed") == false
release_verifier = File.read("scripts/verify-release-manifest.sh")
abort("release verifier does not require legacy live admission evidence") unless release_verifier.include?("legacy_postgres_migration_and_grant_boundary") && release_verifier.include?("legacy_compatibility.enabled == true")
puts "validated #{files.length} YAML files, workload inventory, TLS bridges, rate limits, and closed edge routes"
RUBY

jq empty deploy/helm/merchant-platform/values.schema.json
jq empty deploy/runtime-contracts.json
jq empty deploy/nats/*.json
jq empty infra/retention/*.json
jq empty deploy/release/*.json

ruby <<'RUBY'
ups = Dir["backend/migrations/*.up.sql"].map { |path| File.basename(path) }.sort
downs = Dir["backend/migrations/*.down.sql"].map { |path| File.basename(path) }.sort
abort("financial/retention migrations 000014/000015 are missing") unless ups.any? { |name| name.start_with?("000014_") } && ups.any? { |name| name.start_with?("000015_") } && downs.any? { |name| name.start_with?("000014_") } && downs.any? { |name| name.start_with?("000015_") }
abort("legacy compatibility migration 000018 is missing") unless ups.include?("000018_legacy_compatibility.up.sql") && downs.include?("000018_legacy_compatibility.down.sql")
abort("migration up/down inventory differs") unless !ups.empty? && ups.length == downs.length
ups.each_with_index do |name, index|
  number = format("%06d", index + 1)
  abort("non-contiguous up migration: #{name}") unless name.start_with?(number + "_") && name.end_with?(".up.sql")
  down = downs[index]
  abort("non-contiguous down migration: #{down}") unless down.start_with?(number + "_") && down.end_with?(".down.sql")
end
RUBY

if rg -n 'GRANT .* ON ALL (TABLES|SEQUENCES)|ALTER ROLE merchant_(api|management|admin|financial_runtime).*BYPASSRLS' deploy/postgres; then
  echo "runtime database grants are broader than admitted" >&2
  exit 1
fi
for contract in platform_outbox_publisher rate_runtime_worker merchant_matching_worker merchant_financial_worker merchant_reconciliation_worker merchant_settings_api_runtime merchant_session_revocation_worker merchant_invitation_delivery_worker retention_archive_worker retention_control_scheduler merchant_provider_health_worker legacy_compat_runtime legacy_compat_admission_requester legacy_compat_admission_approver migration_control_worker migration_traffic_actuator; do
  rg -q "$contract" deploy/postgres/bootstrap-roles.sql deploy/postgres/runtime-grants.sql || { echo "database role is not converged: $contract" >&2; exit 1; }
done

if rg -n --glob '!validate.sh' --glob '!manifest.example.json' \
  'pza__[A-Za-z0-9_-]{20,}|-----BEGIN (RSA|EC|OPENSSH) PRIVATE KEY-----|DRAFT_REPLACE_BEFORE_RELEASE' \
  deploy infra docs/adr docs/runbooks .dockerignore; then
  echo "a forbidden credential or release placeholder was found outside its template" >&2
  exit 1
fi

if [[ "$validation_mode" == static ]]; then
  echo "static deployment validation passed"
  exit 0
fi

for command_name in helm kubeconform docker; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required for full validation" >&2; exit 1; }
done
if helm template merchant "$chart_path" --namespace merchant >/dev/null; then
  echo "closed default Helm values were unexpectedly admitted without database peers" >&2
  exit 1
fi
helm lint "$chart_path" --values "$chart_path/ci-values.yaml"
rendered_manifest="$(mktemp -t merchant-platform-rendered.XXXXXX.yaml)"
helm template merchant "$chart_path" --namespace merchant --values "$chart_path/ci-values.yaml" >"$rendered_manifest"
kubeconform -strict -summary "$rendered_manifest"
rm -f "$rendered_manifest"
POSTGRES_PASSWORD=ci docker compose -f infra/compose.yaml config --quiet
echo "full deployment validation passed"
