package topology

type Topology struct {
	SchemaVersion  int         `json:"schema_version" yaml:"schema_version"`
	DeploymentName string      `json:"deployment_name" yaml:"deployment_name"`
	ReleaseChannel string      `json:"release_channel" yaml:"release_channel"`
	Components     *Components `json:"components" yaml:"components"`
	Cloudflare     *Cloudflare `json:"cloudflare" yaml:"cloudflare"`
	Aggregator     *Aggregator `json:"aggregator" yaml:"aggregator"`
	MiSub          *MiSub      `json:"misub" yaml:"misub"`
	Local          *Local      `json:"local" yaml:"local"`
	Secrets        *Secrets    `json:"secrets" yaml:"secrets"`
}

type Components struct {
	Aggregator     *bool `json:"aggregator" yaml:"aggregator"`
	MiSub          *bool `json:"misub" yaml:"misub"`
	ECHWorker      *bool `json:"ech_worker" yaml:"ech_worker"`
	LocalEasyProxy *bool `json:"local_easyproxy" yaml:"local_easyproxy"`
}

type Cloudflare struct {
	AccountIDEnv  string          `json:"account_id_env" yaml:"account_id_env"`
	ZoneIDEnv     string          `json:"zone_id_env" yaml:"zone_id_env"`
	UsePagesDev   *bool           `json:"use_pages_dev" yaml:"use_pages_dev"`
	UseWorkersDev *bool           `json:"use_workers_dev" yaml:"use_workers_dev"`
	Resources     *CloudResources `json:"resources" yaml:"resources"`
}

type CloudResources struct {
	PagesProject    string `json:"pages_project" yaml:"pages_project"`
	D1Database      string `json:"d1_database" yaml:"d1_database"`
	ECHWorker       string `json:"ech_worker" yaml:"ech_worker"`
	R2Bucket        string `json:"r2_bucket" yaml:"r2_bucket"`
	R2PublicBaseURL string `json:"r2_public_base_url" yaml:"r2_public_base_url"`
}

type Aggregator struct {
	Schedule string `json:"schedule" yaml:"schedule"`
}

type MiSub struct {
	DefaultProfile string `json:"default_profile" yaml:"default_profile"`
}

type Local struct {
	InstallMode    string `json:"install_mode" yaml:"install_mode"`
	AccessMode     string `json:"access_mode" yaml:"access_mode"`
	ReleaseChannel string `json:"release_channel" yaml:"release_channel"`
}

type Secrets struct {
	CloudflareAPIToken string `json:"cloudflare_api_token" yaml:"cloudflare_api_token"`
	CloudflareDNSToken string `json:"cloudflare_dns_token" yaml:"cloudflare_dns_token"`
	MiSubAdminPassword string `json:"misub_admin_password" yaml:"misub_admin_password"`
	MiSubCookieSecret  string `json:"misub_cookie_secret" yaml:"misub_cookie_secret"`
	MiSubManifestToken string `json:"misub_manifest_token" yaml:"misub_manifest_token"`
	MiSubCronSecret    string `json:"misub_cron_secret" yaml:"misub_cron_secret"`
	ECHToken           string `json:"ech_token" yaml:"ech_token"`
	R2AccessKeyID      string `json:"r2_access_key_id" yaml:"r2_access_key_id"`
	R2SecretAccessKey  string `json:"r2_secret_access_key" yaml:"r2_secret_access_key"`
}

func (c *Components) CloudEnabled() bool {
	return boolValue(c.Aggregator) || boolValue(c.MiSub) || boolValue(c.ECHWorker)
}

func (c *Components) AnyEnabled() bool {
	return c.CloudEnabled() || boolValue(c.LocalEasyProxy)
}

func (c *Components) AggregatorEnabled() bool {
	return boolValue(c.Aggregator)
}

func (c *Components) MiSubEnabled() bool {
	return boolValue(c.MiSub)
}

func (c *Components) ECHWorkerEnabled() bool {
	return boolValue(c.ECHWorker)
}

func (c *Components) LocalEasyProxyEnabled() bool {
	return boolValue(c.LocalEasyProxy)
}

func boolValue(value *bool) bool {
	return value != nil && *value
}
