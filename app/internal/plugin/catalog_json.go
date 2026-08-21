package plugin

import "encoding/json"

// MarshalJSON keeps required Checks catalog collections stable at the API
// boundary. Go nil slices are an implementation detail; catalog clients must
// always receive JSON arrays for required list fields.
func (metadata CheckMetadata) MarshalJSON() ([]byte, error) {
	type checkMetadataJSON CheckMetadata
	value := checkMetadataJSON(metadata)
	if value.SupportedSystems == nil {
		value.SupportedSystems = []string{}
	}
	if value.Parameters == nil {
		value.Parameters = []Parameter{}
	}
	return json.Marshal(value)
}

// MarshalJSON keeps bundle membership shape stable even for an empty bundle
// value used by tests or future catalog construction paths.
func (bundle CheckBundle) MarshalJSON() ([]byte, error) {
	type checkBundleJSON CheckBundle
	value := checkBundleJSON(bundle)
	if value.CheckIDs == nil {
		value.CheckIDs = []string{}
	}
	return json.Marshal(value)
}
