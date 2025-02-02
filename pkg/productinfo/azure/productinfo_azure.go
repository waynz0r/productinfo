package azure

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/services/compute/mgmt/2018-04-01/compute"
	"github.com/Azure/azure-sdk-for-go/services/preview/commerce/mgmt/2015-06-01-preview/commerce"
	"github.com/Azure/azure-sdk-for-go/services/resources/mgmt/2016-06-01/subscriptions"
	"github.com/Azure/go-autorest/autorest/azure"
	"github.com/Azure/go-autorest/autorest/azure/auth"
	"github.com/banzaicloud/productinfo/pkg/productinfo"
	log "github.com/sirupsen/logrus"
)

const (
	cpu    = "cpu"
	memory = "memory"
)

var (
	regionCodeMappings = map[string]string{
		"ap": "asia",
		"au": "australia",
		"br": "brazil",
		"ca": "canada",
		"eu": "europe",
		"fr": "france",
		"in": "india",
		"ja": "japan",
		"kr": "korea",
		"uk": "uk",
		"us": "us",
	}

	mtBasic, _     = regexp.Compile("^BASIC.A\\d+[_Promo]*$")
	mtStandardA, _ = regexp.Compile("^A\\d+[_Promo]*$")
	mtStandardB, _ = regexp.Compile("^Standard_B\\d+m?[_v\\d]*[_Promo]*$")
	mtStandardD, _ = regexp.Compile("^Standard_D\\d[_v\\d]*[_Promo]*$")
	mtStandardE, _ = regexp.Compile("^Standard_E\\d+i?[_v\\d]*[_Promo]*$")
	mtStandardF, _ = regexp.Compile("^Standard_F\\d+[_v\\d]*[_Promo]*$")
	mtStandardG, _ = regexp.Compile("^Standard_G\\d+[_v\\d]*[_Promo]*$")
	mtStandardL, _ = regexp.Compile("^Standard_L\\d+[_v\\d]*[_Promo]*$")
	mtStandardM, _ = regexp.Compile("^Standard_M\\d+[m|t|l]*s[_v\\d]*[_Promo]*$")
	mtStandardN, _ = regexp.Compile("^Standard_N[C|D|V]\\d+r?[_v\\d]*[_Promo]*$")
)

// AzureInfoer encapsulates the data and operations needed to access external Azure resources
type AzureInfoer struct {
	subscriptionId      string
	subscriptionsClient subscriptions.Client
	vmSizesClient       compute.VirtualMachineSizesClient
	rateCardClient      commerce.RateCardClient
}

// NewAzureInfoer creates a new instance of the Azure infoer
func NewAzureInfoer(subscriptionId string) (*AzureInfoer, error) {
	authorizer, err := auth.NewAuthorizerFromFile(azure.PublicCloud.ResourceManagerEndpoint)
	if err != nil {
		return nil, err
	}

	sClient := subscriptions.NewClient()
	sClient.Authorizer = authorizer

	vmClient := compute.NewVirtualMachineSizesClient(subscriptionId)
	vmClient.Authorizer = authorizer

	rcClient := commerce.NewRateCardClient(subscriptionId)
	rcClient.Authorizer = authorizer

	return &AzureInfoer{
		subscriptionId:      subscriptionId,
		subscriptionsClient: sClient,
		vmSizesClient:       vmClient,
		rateCardClient:      rcClient,
	}, nil
}

type regionParts []string

func (r regionParts) String() string {
	var result string
	for _, p := range r {
		result += p
	}
	return result
}

func (a *AzureInfoer) toRegionID(meterRegion string, regions map[string]string) (string, error) {
	var rp regionParts = strings.Split(strings.ToLower(meterRegion), " ")
	regionCode := regionCodeMappings[rp[0]]
	lastPart := rp[len(rp)-1]
	var regionIds []string
	if _, err := strconv.Atoi(lastPart); err == nil {
		regionIds = []string{
			fmt.Sprintf("%s%s%s", regionCode, rp[1:len(rp)-1], lastPart),
			fmt.Sprintf("%s%s%s", rp[1:len(rp)-1], regionCode, lastPart),
		}
	} else {
		regionIds = []string{
			fmt.Sprintf("%s%s", regionCode, rp[1:]),
			fmt.Sprintf("%s%s", rp[1:], regionCode),
		}
	}
	for _, regionID := range regionIds {
		if a.checkRegionID(regionID, regions) {
			return regionID, nil
		}
	}
	return "", fmt.Errorf("couldn't find region: %s", meterRegion)
}

func (a *AzureInfoer) checkRegionID(regionID string, regions map[string]string) bool {
	for region := range regions {
		if regionID == region {
			return true
		}
	}
	return false
}

// Initialize downloads and parses the Rate Card API's meter list on Azure
func (a *AzureInfoer) Initialize() (map[string]map[string]productinfo.Price, error) {
	log.Debug("initializing Azure price info")
	allPrices := make(map[string]map[string]productinfo.Price)

	regions, err := a.GetRegions()
	if err != nil {
		return nil, err
	}

	log.Debugf("queried regions: %v", regions)

	rateCardFilter := "OfferDurableId eq 'MS-AZR-0003p' and Currency eq 'USD' and Locale eq 'en-US' and RegionInfo eq 'US'"
	result, err := a.rateCardClient.Get(context.TODO(), rateCardFilter)
	if err != nil {
		return nil, err
	}
	for _, v := range *result.Meters {
		if *v.MeterCategory == "Virtual Machines" && len(*v.MeterTags) == 0 && *v.MeterRegion != "" {
			if !strings.Contains(*v.MeterSubCategory, "(Windows)") {
				region, err := a.toRegionID(*v.MeterRegion, regions)
				if err != nil {
					log.Debugf(err.Error())
					continue
				}
				var instanceType string
				category := strings.Split(*v.MeterSubCategory, " ")
				if len(category) < 2 {
					log.Debugf("couldn't parse meter sub category: %s, region=%s", *v.MeterSubCategory, *v.MeterRegion)
					continue
				}
				switch category[1] {
				case "VM":
					instanceType = category[0]
				case "VM_Promo":
					instanceType = category[0] + "_Promo"
				default:
					log.Debugf("instance type is empty: %s, region=%s", *v.MeterSubCategory, *v.MeterRegion)
					continue
				}
				instanceType = a.transformMachineType(instanceType)

				var priceInUsd float64

				if len(v.MeterRates) < 1 {
					log.Debugf("%s doesn't have rate info in region %s", *v.MeterSubCategory, *v.MeterRegion)
					continue
				}
				for _, rate := range v.MeterRates {
					priceInUsd += *rate
				}
				if allPrices[region] == nil {
					allPrices[region] = make(map[string]productinfo.Price)
				}
				price := allPrices[region][instanceType]
				if !strings.Contains(*v.MeterSubCategory, "Low Priority") {
					price.OnDemandPrice = priceInUsd
				} else {
					spotPrice := make(productinfo.SpotPriceInfo)
					spotPrice[region] = priceInUsd
					price.SpotPrice = spotPrice
				}

				allPrices[region][instanceType] = price
				log.Debugf("price info added: [region=%s, machinetype=%s, price=%v]", region, instanceType, price)
				mts := a.getMachineTypeVariants(instanceType)
				for _, mt := range mts {
					allPrices[region][mt] = price
					log.Debugf("price info added: [region=%s, machinetype=%s, price=%v]", region, mt, price)
				}
			}
		}
	}

	log.Debug("finished initializing Azure price info")
	return allPrices, nil
}

func (a *AzureInfoer) transformMachineType(mt string) string {
	switch {
	case mtBasic.MatchString(mt):
		parts := strings.Split(mt, ".")
		return strings.Title(strings.ToLower(parts[0])) + "_" + parts[1]
	case mtStandardA.MatchString(mt):
		return "Standard_" + mt
	}
	return mt
}

func (a *AzureInfoer) getMachineTypeVariants(mt string) []string {
	switch {
	case mtStandardB.MatchString(mt):
		return []string{mt + "s"}
	case mtStandardD.MatchString(mt):
		result := make([]string, 6)
		result[0] = a.addSuffix(mt, "s")[0]
		dsType := strings.Replace(mt, "Standard_D", "Standard_DS", -1)
		result[1] = dsType
		for i, s := range a.addSuffix(dsType, "-1", "-2", "-4", "-8") {
			result[i+2] = s
		}
		return result
	case mtStandardE.MatchString(mt):
		result := make([]string, 6)
		for i, s := range a.addSuffix(mt, "s", "-2s", "-4s", "-8s", "-16s", "-32s") {
			result[i] = s
		}
		return result
	case mtStandardF.MatchString(mt):
		return a.addSuffix(mt, "s")
	case mtStandardG.MatchString(mt):
		result := make([]string, 4)
		gsType := strings.Replace(mt, "Standard_G", "Standard_GS", -1)
		result[0] = gsType
		for i, s := range a.addSuffix(gsType, "-4", "-8", "-16") {
			result[i+1] = s
		}
		return result
	case mtStandardL.MatchString(mt):
		return a.addSuffix(mt, "s")
	case mtStandardM.MatchString(mt) && strings.HasSuffix(mt, "ms"):
		base := strings.TrimSuffix(mt, "ms")
		return a.addSuffix(base, "-2ms", "-4ms", "-8ms", "-16ms", "-32ms")
	case mtStandardM.MatchString(mt) && (strings.HasSuffix(mt, "ls") || strings.HasSuffix(mt, "ts")):
		return []string{}
	case mtStandardM.MatchString(mt) && strings.HasSuffix(mt, "s"):
		base := strings.TrimSuffix(mt, "s")
		return a.addSuffix(base, "", "m")
	case mtStandardN.MatchString(mt):
		return a.addSuffix(mt, "s")
	}

	return []string{}
}

func (a *AzureInfoer) addSuffix(mt string, suffixes ...string) []string {
	result := make([]string, len(suffixes))
	var suffix string
	parts := strings.Split(mt, "_")
	if len(parts) > 2 {
		for _, p := range parts[2:] {
			suffix += "_" + p
		}
	}
	for i, s := range suffixes {
		result[i] = parts[0] + "_" + parts[1] + s + suffix
	}
	return result
}

// GetAttributeValues gets the AttributeValues for the given attribute name
func (a *AzureInfoer) GetAttributeValues(attribute string) (productinfo.AttrValues, error) {

	log.Debugf("getting %s values", attribute)

	values := make(productinfo.AttrValues, 0)
	valueSet := make(map[productinfo.AttrValue]interface{})

	regions, err := a.GetRegions()
	if err != nil {
		return nil, err
	}

	for region := range regions {
		vmSizes, err := a.vmSizesClient.List(context.TODO(), region)
		if err != nil {
			log.WithError(err).Warnf("[Azure] couldn't get VM sizes in region %s", region)
			continue
		}
		for _, v := range *vmSizes.Value {
			switch attribute {
			case cpu:
				valueSet[productinfo.AttrValue{
					Value:    float64(*v.NumberOfCores),
					StrValue: fmt.Sprintf("%v", *v.NumberOfCores),
				}] = ""
			case memory:
				valueSet[productinfo.AttrValue{
					Value:    float64(*v.MemoryInMB) / 1024,
					StrValue: fmt.Sprintf("%v", *v.MemoryInMB),
				}] = ""
			}
		}
	}

	for attr := range valueSet {
		values = append(values, attr)
	}

	log.Debugf("found %s values: %v", attribute, values)
	return values, nil
}

// GetProducts retrieves the available virtual machines based on the arguments provided
func (a *AzureInfoer) GetProducts(regionId string) ([]productinfo.VmInfo, error) {
	log.Debugf("getting product info [region=%s]", regionId)
	var vms []productinfo.VmInfo
	vmSizes, err := a.vmSizesClient.List(context.TODO(), regionId)
	if err != nil {
		return nil, err
	}
	for _, v := range *vmSizes.Value {
		vms = append(vms, productinfo.VmInfo{
			Type: *v.Name,
			Cpus: float64(*v.NumberOfCores),
			Mem:  float64(*v.MemoryInMB) / 1024,
			// TODO: netw perf
		})
	}

	log.Debugf("found vms: %#v", vms)
	return vms, nil
}

// GetZones returns the availability zones in a region
func (a *AzureInfoer) GetZones(region string) ([]string, error) {
	return []string{region}, nil
}

// GetRegions returns a map with available regions transforms the api representation into a "plain" map
func (a *AzureInfoer) GetRegions() (map[string]string, error) {
	regions := make(map[string]string)
	locations, err := a.subscriptionsClient.ListLocations(context.TODO(), a.subscriptionId)
	if err != nil {
		return nil, err
	}
	for _, loc := range *locations.Value {
		regions[*loc.Name] = *loc.DisplayName
	}
	return regions, nil
}

// HasShortLivedPriceInfo - Azure doesn't have frequently changing prices
func (a *AzureInfoer) HasShortLivedPriceInfo() bool {
	return false
}

// GetCurrentPrices retrieves all the price info in a region
func (a *AzureInfoer) GetCurrentPrices(region string) (map[string]productinfo.Price, error) {
	return nil, errors.New("azure prices cannot be queried on the fly")
}

// GetMemoryAttrName returns the provider representation of the memory attribute
func (a *AzureInfoer) GetMemoryAttrName() string {
	return memory
}

// GetCpuAttrName returns the provider representation of the cpu attribute
func (a *AzureInfoer) GetCpuAttrName() string {
	return cpu
}

// GetNetworkPerformanceMapper returns the network performance mappier implementation for this provider
func (a *AzureInfoer) GetNetworkPerformanceMapper() (productinfo.NetworkPerfMapper, error) {
	return newAzureNetworkMapper(), nil
}
