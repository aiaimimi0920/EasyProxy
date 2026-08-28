package topology

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

var (
	deploymentNamePattern = regexp.MustCompile(`^[a-z][a-z0-9-]{1,47}[a-z0-9]$`)
	resourceNamePattern   = regexp.MustCompile(`^$|^[a-z](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
	envNamePattern        = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
)

func (t *Topology) Validate() error {
	if t.SchemaVersion != 1 {
		return fmt.Errorf("schema_version must be 1, got %d", t.SchemaVersion)
	}
	if !deploymentNamePattern.MatchString(t.DeploymentName) {
		return errors.New("deployment_name must match ^[a-z][a-z0-9-]{1,47}[a-z0-9]$")
	}
	if err := validateChannel("release_channel", t.ReleaseChannel); err != nil {
		return err
	}
	if err := validateComponents(t.Components); err != nil {
		return err
	}
	if err := validateCloudflare(t.Cloudflare); err != nil {
		return err
	}
	if t.Aggregator == nil || len(strings.Fields(t.Aggregator.Schedule)) != 5 {
		return errors.New("aggregator.schedule must be a non-empty five-field cron expression")
	}
	if t.MiSub == nil || strings.TrimSpace(t.MiSub.DefaultProfile) == "" {
		return errors.New("misub.default_profile is required")
	}
	if err := validateLocal(t.Local); err != nil {
		return err
	}
	return validateSecrets(t.Secrets)
}

func validateComponents(value *Components) error {
	if value == nil {
		return errors.New("components is required")
	}
	if value.Aggregator == nil || value.MiSub == nil || value.ECHWorker == nil || value.LocalEasyProxy == nil {
		return errors.New("all component switches are required")
	}
	if !value.AnyEnabled() {
		return errors.New("at least one component must be enabled")
	}
	return nil
}

func validateCloudflare(value *Cloudflare) error {
	if value == nil {
		return errors.New("cloudflare is required")
	}
	if value.UsePagesDev == nil || value.UseWorkersDev == nil || value.Resources == nil {
		return errors.New("cloudflare use flags and resources are required")
	}
	if !envNamePattern.MatchString(value.AccountIDEnv) {
		return errors.New("cloudflare.account_id_env must be an environment variable name")
	}
	if value.ZoneIDEnv != "" && !envNamePattern.MatchString(value.ZoneIDEnv) {
		return errors.New("cloudflare.zone_id_env must be empty or an environment variable name")
	}
	resources := value.Resources
	resourceNames := []struct {
		field string
		name  string
	}{
		{"pages_project", resources.PagesProject},
		{"d1_database", resources.D1Database},
		{"ech_worker", resources.ECHWorker},
		{"r2_bucket", resources.R2Bucket},
	}
	for _, resource := range resourceNames {
		if !resourceNamePattern.MatchString(resource.name) {
			return fmt.Errorf("cloudflare.resources.%s has an invalid resource name", resource.field)
		}
	}
	if resources.R2PublicBaseURL != "" {
		parsed, err := url.ParseRequestURI(resources.R2PublicBaseURL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
			return errors.New("cloudflare.resources.r2_public_base_url must be empty or an HTTPS URL without credentials")
		}
	}
	return nil
}

func validateLocal(value *Local) error {
	if value == nil {
		return errors.New("local is required")
	}
	if value.InstallMode != "docker" && value.InstallMode != "native" {
		return errors.New("local.install_mode must be docker or native")
	}
	switch value.AccessMode {
	case "local_server", "pool", "gateway":
	default:
		return errors.New("local.access_mode must be local_server, pool, or gateway")
	}
	return validateChannel("local.release_channel", value.ReleaseChannel)
}

func validateSecrets(value *Secrets) error {
	if value == nil {
		return errors.New("secrets is required")
	}
	required := []struct {
		field   string
		envName string
	}{
		{"cloudflare_api_token", value.CloudflareAPIToken},
		{"misub_admin_password", value.MiSubAdminPassword},
		{"misub_cookie_secret", value.MiSubCookieSecret},
		{"misub_manifest_token", value.MiSubManifestToken},
		{"misub_cron_secret", value.MiSubCronSecret},
		{"ech_token", value.ECHToken},
		{"r2_access_key_id", value.R2AccessKeyID},
		{"r2_secret_access_key", value.R2SecretAccessKey},
	}
	for _, reference := range required {
		if !envNamePattern.MatchString(reference.envName) {
			return fmt.Errorf("secrets.%s must be an environment variable name", reference.field)
		}
	}
	if value.CloudflareDNSToken != "" && !envNamePattern.MatchString(value.CloudflareDNSToken) {
		return errors.New("secrets.cloudflare_dns_token must be empty or an environment variable name")
	}
	return nil
}

func validateChannel(field, value string) error {
	if value != "stable" && value != "candidate" {
		return fmt.Errorf("%s must be stable or candidate", field)
	}
	return nil
}
