package google

import (
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform/helper/schema"
	computeBeta "google.golang.org/api/compute/v0.beta"
)

func expandAliasIpRanges(ranges []interface{}) []*computeBeta.AliasIpRange {
	ipRanges := make([]*computeBeta.AliasIpRange, 0, len(ranges))
	for _, raw := range ranges {
		data := raw.(map[string]interface{})
		ipRanges = append(ipRanges, &computeBeta.AliasIpRange{
			IpCidrRange:         data["ip_cidr_range"].(string),
			SubnetworkRangeName: data["subnetwork_range_name"].(string),
		})
	}
	return ipRanges
}

func flattenAliasIpRange(ranges []*computeBeta.AliasIpRange) []map[string]interface{} {
	rangesSchema := make([]map[string]interface{}, 0, len(ranges))
	for _, ipRange := range ranges {
		rangesSchema = append(rangesSchema, map[string]interface{}{
			"ip_cidr_range":         ipRange.IpCidrRange,
			"subnetwork_range_name": ipRange.SubnetworkRangeName,
		})
	}
	return rangesSchema
}

func flattenScheduling(scheduling *computeBeta.Scheduling) []map[string]interface{} {
	result := make([]map[string]interface{}, 0, 1)
	schedulingMap := map[string]interface{}{
		"on_host_maintenance": scheduling.OnHostMaintenance,
		"preemptible":         scheduling.Preemptible,
	}
	if scheduling.AutomaticRestart != nil {
		schedulingMap["automatic_restart"] = *scheduling.AutomaticRestart
	}
	result = append(result, schedulingMap)
	return result
}

func getProjectAndRegionFromSubnetworkLink(subnetwork string) (string, string) {
	r := regexp.MustCompile(SubnetworkLinkRegex)
	if !r.MatchString(subnetwork) {
		return "", ""
	}

	matches := r.FindStringSubmatch(subnetwork)
	return matches[1], matches[2]
}

func flattenAccessConfigs(accessConfigs []*computeBeta.AccessConfig) ([]map[string]interface{}, string) {
	flattened := make([]map[string]interface{}, len(accessConfigs))
	natIP := ""
	for i, ac := range accessConfigs {
		flattened[i] = map[string]interface{}{
			"nat_ip":          ac.NatIP,
			"assigned_nat_ip": ac.NatIP,
		}
		if natIP == "" {
			natIP = ac.NatIP
		}
	}
	return flattened, natIP
}

func flattenNetworkInterfaces(networkInterfaces []*computeBeta.NetworkInterface) ([]map[string]interface{}, string, string, string) {
	flattened := make([]map[string]interface{}, len(networkInterfaces))
	var region, internalIP, externalIP string

	for i, iface := range networkInterfaces {
		var ac []map[string]interface{}
		ac, externalIP = flattenAccessConfigs(iface.AccessConfigs)

		var project string
		project, region = getProjectAndRegionFromSubnetworkLink(iface.Subnetwork)

		flattened[i] = map[string]interface{}{
			"address":            iface.NetworkIP,
			"network_ip":         iface.NetworkIP,
			"network":            iface.Network,
			"subnetwork":         iface.Subnetwork,
			"subnetwork_project": project,
			"access_config":      ac,
			"alias_ip_range":     flattenAliasIpRange(iface.AliasIpRanges),
		}
		// Instance template interfaces never have names, so they're absent
		// in the instance template network_interface schema. We want to use the
		// same flattening code for both resource types, so we avoid trying to
		// set the name field when it's not set at the GCE end.
		if iface.Name != "" {
			flattened[i]["name"] = iface.Name
		}
		if internalIP == "" {
			internalIP = iface.NetworkIP
		}
	}
	return flattened, region, internalIP, externalIP
}

func expandAccessConfigs(configs []interface{}) []*computeBeta.AccessConfig {
	acs := make([]*computeBeta.AccessConfig, len(configs))
	for i, raw := range configs {
		data := raw.(map[string]interface{})
		acs[i] = &computeBeta.AccessConfig{
			Type:  "ONE_TO_ONE_NAT",
			NatIP: data["nat_ip"].(string),
		}
	}
	return acs
}

func expandNetworkInterfaces(d *schema.ResourceData, config *Config) ([]*computeBeta.NetworkInterface, error) {
	project, err := getProject(d, config)
	if err != nil {
		return nil, err
	}
	region, err := getRegion(d, config)
	if err != nil {
		return nil, err
	}

	configs := d.Get("network_interface").([]interface{})
	ifaces := make([]*computeBeta.NetworkInterface, len(configs))
	for i, raw := range configs {
		data := raw.(map[string]interface{})

		network := data["network"].(string)
		subnetwork := data["subnetwork"].(string)
		if (network == "" && subnetwork == "") || (network != "" && subnetwork != "") {
			return nil, fmt.Errorf("exactly one of network or subnetwork must be provided")
		}

		nf, err := ParseNetworkFieldValue(network, d, config)
		if err != nil {
			return nil, fmt.Errorf("cannot determine selflink for subnetwork '%s': %s", subnetwork, err)
		}

		subnetworkProject := data["subnetwork_project"].(string)
		subnetLink, err := getSubnetworkLink(config, project, region, subnetworkProject, subnetwork)
		if err != nil {
			return nil, fmt.Errorf("cannot determine selflink for subnetwork '%s': %s", subnetwork, err)
		}

		ifaces[i] = &computeBeta.NetworkInterface{
			NetworkIP:     data["network_ip"].(string),
			Network:       nf.RelativeLink(),
			Subnetwork:    subnetLink,
			AccessConfigs: expandAccessConfigs(data["access_config"].([]interface{})),
			AliasIpRanges: expandAliasIpRanges(data["alias_ip_range"].([]interface{})),
		}

		// network_ip is deprecated. We want address to win if both are set.
		if data["address"].(string) != "" {
			ifaces[i].NetworkIP = data["address"].(string)
		}

	}
	return ifaces, nil
}

func flattenServiceAccounts(serviceAccounts []*computeBeta.ServiceAccount) []map[string]interface{} {
	result := make([]map[string]interface{}, len(serviceAccounts))
	for i, serviceAccount := range serviceAccounts {
		result[i] = map[string]interface{}{
			"email":  serviceAccount.Email,
			"scopes": schema.NewSet(stringScopeHashcode, convertStringArrToInterface(serviceAccount.Scopes)),
		}
	}
	return result
}

func expandServiceAccounts(configs []interface{}) []*computeBeta.ServiceAccount {
	accounts := make([]*computeBeta.ServiceAccount, len(configs))
	for i, raw := range configs {
		data := raw.(map[string]interface{})

		accounts[i] = &computeBeta.ServiceAccount{
			Email:  data["email"].(string),
			Scopes: canonicalizeServiceScopes(convertStringSet(data["scopes"].(*schema.Set))),
		}

		if accounts[i].Email == "" {
			accounts[i].Email = "default"
		}
	}
	return accounts
}

func flattenGuestAccelerators(accelerators []*computeBeta.AcceleratorConfig) []map[string]interface{} {
	acceleratorsSchema := make([]map[string]interface{}, len(accelerators))
	for i, accelerator := range accelerators {
		acceleratorsSchema[i] = map[string]interface{}{
			"count": accelerator.AcceleratorCount,
			"type":  accelerator.AcceleratorType,
		}
	}
	return acceleratorsSchema
}

func resourceInstanceTags(d *schema.ResourceData) *computeBeta.Tags {
	// Calculate the tags
	var tags *computeBeta.Tags
	if v := d.Get("tags"); v != nil {
		vs := v.(*schema.Set)
		tags = new(computeBeta.Tags)
		tags.Items = make([]string, vs.Len())
		for i, v := range vs.List() {
			tags.Items[i] = v.(string)
		}

		tags.Fingerprint = d.Get("tags_fingerprint").(string)
	}

	return tags
}