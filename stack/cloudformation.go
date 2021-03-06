package stack

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/goamz/goamz/aws"

	"github.com/litl/galaxy/log"
)

/*
Most of this should probably get wrapped up in a goamz/cloudformations package,
if someone wants to write out the entire API.

TODO: this is going to need some DRY love
TODO: regions are handled with global state, and ENV vars override cli options
TODO: Use SQS instead of polling
*/

var ErrTimeout = fmt.Errorf("timeout")

var Region = "us-east-1"

// thie error type also provides a list of failures from the stack's events
type FailuresError struct {
	messages []string
}

func (f *FailuresError) List() []string {
	return f.messages
}

// The basic Error returns the oldest failure in the list
func (f *FailuresError) Error() string {
	if len(f.messages) == 0 {
		return ""
	}
	return f.messages[len(f.messages)-1]
}

type GetTemplateResponse struct {
	TemplateBody []byte `xml:"GetTemplateResult>TemplateBody"`
}

type CreateStackResponse struct {
	RequestId string `xml:"ResponseMetadata>RequestId"`
	StackId   string `xml:"CreateStackResult>StackId"`
}

type UpdateStackResponse struct {
	RequestId string `xml:"ResponseMetadata>RequestId"`
	StackId   string `xml:"UpdateStackResult>StackId"`
}

type DeleteStackResponse struct {
	RequestId string `xml:"ResponseMetadata>RequestId"`
}

type stackParameter struct {
	Key   string `xml:"ParameterKey"`
	Value string `xml:"ParameterValue"`
}

type stackTag struct {
	Key   string
	Value string
}

type stackDescription struct {
	Id           string           `xml:"StackId"`
	Name         string           `xml:"StackName"`
	Status       string           `xml:"StackStatus"`
	StatusReason string           `xml:"StackStatusReason"`
	Parameters   []stackParameter `xml:"Parameters>member"`
	Tags         []stackTag       `xml:"Tags>member"`
}

type DescribeStacksResponse struct {
	RequestId string             `xml:"ResponseMetadata>RequestId"`
	Stacks    []stackDescription `xml:"DescribeStacksResult>Stacks>member"`
}

type stackResource struct {
	Status     string `xml:"ResourceStatus"`
	LogicalId  string `xml:"LogicalResourceId"`
	PhysicalId string `xml:"PhysicalResourceId"`
	Type       string `xml:"ResourceType"`
}

type ListStackResourcesResponse struct {
	RequestId string          `xml:"ResponseMetadata>RequestId"`
	Resources []stackResource `xml:"ListStackResourcesResult>StackResourceSummaries>member"`
}

type serverCert struct {
	ServerCertificateName string `xml:"ServerCertificateName"`
	Path                  string `xml:"Path"`
	Arn                   string `xml:"Arn"`
	UploadDate            string `xml:"UploadDate"`
	ServerCertificateId   string `xml:"ServerCertificateId"`
	Expiration            string `xml:"Expiration"`
}

type ListServerCertsResponse struct {
	RequestId string       `xml:"ResponseMetadata>RequestId"`
	Certs     []serverCert `xml:"ListServerCertificatesResult>ServerCertificateMetadataList>member"`
}

type stackEvent struct {
	EventId              string
	LogicalResourceId    string
	PhysicalResourceId   string
	ResourceProperties   string
	ResourceStatus       string
	ResourceStatusReason string
	ResourceType         string
	StackId              string
	StackName            string
	Timestamp            time.Time
}

type DescribeStackEventsResult struct {
	Events []stackEvent `xml:"DescribeStackEventsResult>StackEvents>member"`
}

type stackSummary struct {
	CreationTime        time.Time
	DeletionTime        time.Time
	LastUpdatedTime     time.Time
	StackId             string
	StackName           string
	StackStatus         string
	StackStatusReason   string
	TemplateDescription string
}

type ListStacksResponse struct {
	Stacks []stackSummary `xml:"ListStacksResult>StackSummaries>member"`
}

type AvailabilityZoneInfo struct {
	Name   string `xml:"zoneName"`
	State  string `xml:"zoneState"`
	Region string `xml:"regionName"`
}

type DescribeAvailabilityZonesResponse struct {
	RequestId         string                 `xml:"requestId"`
	AvailabilityZones []AvailabilityZoneInfo `xml:"availabilityZoneInfo>item"`
}

type Subnet struct {
	ID                        string `xml:"subnetId"`
	State                     string `xml:"state"`
	VPCID                     string `xml:"vpcId"`
	CIDRBlock                 string `xml:"cidrBlock"`
	AvailableIPAddressesCount int    `xml:"availableIpAddressCount"`
	AvailabilityZone          string `xml:"availabilityZone"`
	DefaultForAZ              bool   `xml:"defaultForAz"`
	MapPublicIPOnLaunch       bool   `xml:"mapPublicIpOnLaunch"`
}

type DescribeSubnetsResponse struct {
	RequestId string   `xml:"requestId"`
	Subnets   []Subnet `xml:"subnetSet>item"`
}

// Resources from the base stack that may need to be referenced from other
// stacks
type SharedResources struct {
	SecurityGroups map[string]string
	Roles          map[string]string
	Parameters     map[string]string
	ServerCerts    map[string]string
	Subnets        []Subnet
	VPCID          string
}

// Return a list of the subnet values.
func (s SharedResources) ListSubnets() []string {
	subnets := []string{}
	for _, val := range s.Subnets {
		subnets = append(subnets, val.ID)
	}
	return subnets
}

func GetAWSRegion(region string) (*aws.Region, error) {
	if region == "" {
		region = os.Getenv("AWS_DEFAULT_REGION")
		if region != "" {
			log.Debugf("Using AWS_DEFAULT_REGION=%s", region)
		}
	}

	// AWS_REGION isn't used by the aws-cli, but check here just in case
	if region == "" {
		region = os.Getenv("AWS_REGION")
		if region != "" {
			log.Debugf("Using AWS_REGION=%s", region)
		}
	}

	if region == "" {
		region = Region
	}

	var reg aws.Region
	for name, r := range aws.Regions {
		if name == region {
			reg = r
		}
	}

	if reg.Name == "" {
		return nil, fmt.Errorf("region %s not found", region)
	}
	return &reg, nil
}

func getService(service, region string) (*aws.Service, error) {

	reg, err := GetAWSRegion(region)
	if err != nil {
		return nil, err
	}

	var endpoint string
	switch service {
	case "cf":
		endpoint = reg.CloudFormationEndpoint
	case "ec2":
		endpoint = reg.EC2Endpoint
	case "iam":
		endpoint = reg.IAMEndpoint
	case "rds":
		endpoint = reg.RDSEndpoint.Endpoint
	default:
		return nil, fmt.Errorf("Service %s not implemented", service)
	}

	// only get the creds from the env for now
	auth, err := aws.GetAuth("", "", "", time.Now())
	if err != nil {
		return nil, err
	}

	serviceInfo := aws.ServiceInfo{
		Endpoint: endpoint,
		Signer:   aws.V2Signature,
	}

	svc, err := aws.NewService(auth, serviceInfo)
	if err != nil {
		return nil, err
	}
	return svc, nil
}

// Lookup and unmarshal an existing stack into a Pool
func GetPool(name string) (*Pool, error) {
	pool := &Pool{}

	poolTmpl, err := GetTemplate(name)
	if err != nil {
		return pool, err
	}

	if err := json.Unmarshal(poolTmpl, pool); err != nil {
		return nil, err
	}

	return pool, nil
}

func GetStackVPC(stackName string) (string, error) {
	stackResp, err := ListStackResources(stackName)
	if err != nil {
		return "", err
	}

	for _, res := range stackResp.Resources {
		if res.Type == "AWS::EC2::VPC" {
			return res.PhysicalId, nil
		}
	}

	return "", fmt.Errorf("No VPC found")
}

func DescribeSubnets(vpcID, region string) (DescribeSubnetsResponse, error) {
	dsnResp := DescribeSubnetsResponse{}

	service, err := getService("ec2", region)
	if err != nil {
		return dsnResp, err
	}

	params := map[string]string{
		"Action":  "DescribeSubnets",
		"Version": "2014-02-01",
	}

	if vpcID != "" {
		params["Filter.1.Name"] = "vpc-id"
		params["Filter.1.Value.1"] = vpcID
	}

	resp, err := service.Query("GET", "/", params)
	if err != nil {
		return dsnResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := service.BuildError(resp)
		return dsnResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&dsnResp)
	if err != nil {
		return dsnResp, err
	}
	return dsnResp, nil
}

func DescribeAvailabilityZones(region string) (DescribeAvailabilityZonesResponse, error) {
	azResp := DescribeAvailabilityZonesResponse{}

	service, err := getService("ec2", region)
	if err != nil {
		return azResp, err
	}

	params := map[string]string{
		"Action":  "DescribeAvailabilityZones",
		"Version": "2014-02-01",
	}

	resp, err := service.Query("GET", "/", params)
	if err != nil {
		return azResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := service.BuildError(resp)
		return azResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&azResp)
	if err != nil {
		return azResp, err
	}
	return azResp, nil
}

// List all resources associated with stackName
func ListStackResources(stackName string) (ListStackResourcesResponse, error) {
	listResp := ListStackResourcesResponse{}

	svc, err := getService("cf", "")
	if err != nil {
		return listResp, err
	}

	params := map[string]string{
		"Action":    "ListStackResources",
		"StackName": stackName,
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return listResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return listResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&listResp)
	if err != nil {
		return listResp, err
	}
	return listResp, nil
}

// Describe all running stacks
func DescribeStacks(name string) (DescribeStacksResponse, error) {
	descResp := DescribeStacksResponse{}

	svc, err := getService("cf", "")
	if err != nil {
		return descResp, err
	}

	params := map[string]string{
		"Action": "DescribeStacks",
	}

	if name != "" {
		params["StackName"] = name
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return descResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return descResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&descResp)
	if err != nil {
		return descResp, err
	}
	return descResp, nil
}

// Describe a Stack's Events
func DescribeStackEvents(name string) (DescribeStackEventsResult, error) {
	descResp := DescribeStackEventsResult{}

	svc, err := getService("cf", "")
	if err != nil {
		return descResp, err
	}

	params := map[string]string{
		"Action": "DescribeStackEvents",
	}

	if name != "" {
		params["StackName"] = name
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return descResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return descResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&descResp)
	if err != nil {
		return descResp, err
	}
	return descResp, nil
}

// return a list of all actives stacks
func ListActive() ([]string, error) {
	resp, err := DescribeStacks("")
	if err != nil {
		return nil, err
	}

	stacks := []string{}
	for _, stack := range resp.Stacks {
		stacks = append(stacks, stack.Name)
	}

	return stacks, nil
}

// List all stacks
// This lists all stacks including inactive and deleted.
func List() (ListStacksResponse, error) {
	listResp := ListStacksResponse{}

	svc, err := getService("cf", "")
	if err != nil {
		return listResp, err
	}

	params := map[string]string{
		"Action": "ListStacks",
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return listResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return listResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&listResp)
	if err != nil {
		return listResp, err
	}
	return listResp, nil

}

func Exists(name string) (bool, error) {
	resp, err := DescribeStacks("")
	if err != nil {
		return false, err
	}

	for _, stack := range resp.Stacks {
		if stack.Name == name {
			return true, nil
		}
	}

	return false, nil
}

// Wait for a stack event to complete.
// Poll every 5s while the stack is in the CREATE_IN_PROGRESS or
// UPDATE_IN_PROGRESS state, and succeed when it enters a successful _COMPLETE
// state.
// Return and error of ErrTimeout if the timeout is reached.
func Wait(name string, timeout time.Duration) error {
	start := time.Now()
	deadline := start.Add(timeout)
	for {
		resp, err := DescribeStacks(name)
		if err != nil {
			if err, ok := err.(*aws.Error); ok {
				// the call was successful, but AWS returned an error
				// no need to wait.
				return err
			}

			// I guess we should sleep and retry here, in case of intermittent
			// errors
			log.Errorln("DescribeStacks:", err)
			goto SLEEP
		}

		for _, stack := range resp.Stacks {
			if stack.Name == name {
				switch stack.Status {
				case "CREATE_IN_PROGRESS", "UPDATE_IN_PROGRESS":
					goto SLEEP
				case "CREATE_COMPLETE", "UPDATE_COMPLETE", "UPDATE_COMPLETE_CLEANUP_IN_PROGRESS":
					return nil
				default:
					// see if we can caught the actual FAILURE
					// start looking slightly before we started the watch.
					// We're more likely to catch a quick event than we are to
					// pickup something from a previous transaction.
					failures, _ := ListFailures(name, start.Add(-2*time.Second))
					if len(failures) > 0 {
						return &FailuresError{
							messages: failures,
						}
					}

					// we didn't catch the events for some reason, return our current status
					return fmt.Errorf("%s: %s", stack.Status, stack.StatusReason)
				}
			}
		}

	SLEEP:
		if time.Now().After(deadline) {
			return ErrTimeout
		}

		time.Sleep(5 * time.Second)
	}
}

// List failures on a stack as "STATUS:REASON"
func ListFailures(id string, since time.Time) ([]string, error) {
	resp, err := DescribeStackEvents(id)
	if err != nil {
		return nil, err
	}

	fails := []string{}

	for _, event := range resp.Events {
		status, reason := event.ResourceStatus, event.ResourceStatusReason
		if event.Timestamp.After(since) && strings.HasSuffix(status, "_FAILED") {
			fails = append(fails, fmt.Sprintf("%s: %s", status, reason))
		}
	}

	return fails, nil
}

// Like the Wait function, but instead if returning as soon as there is an
// error, always wait for a final status.
// ** This assumes all _COMPLETE statuses are final, and all final statuses end
//    in _COMPLETE.
func WaitForComplete(id string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		resp, err := DescribeStacks(id)
		if err != nil {
			return err
		} else if len(resp.Stacks) != 1 {
			return fmt.Errorf("could not find stack: %s", id)
		}

		stack := resp.Stacks[0]

		if strings.HasSuffix(stack.Status, "_COMPLETE") {
			return nil
		}

		if time.Now().After(deadline) {
			return ErrTimeout
		}

		time.Sleep(5 * time.Second)
	}
}

// Get a list of SSL certificates from the IAM service.
// Cloudformation templates need to reference certs via their ARNs.
func ListServerCertificates() (ListServerCertsResponse, error) {
	certResp := ListServerCertsResponse{}

	svc, err := getService("iam", "")
	if err != nil {
		return certResp, err
	}

	params := map[string]string{
		"Action":  "ListServerCertificates",
		"Version": "2010-05-08",
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return certResp, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return certResp, err
	}
	defer resp.Body.Close()

	err = xml.NewDecoder(resp.Body).Decode(&certResp)
	if err != nil {
		return certResp, err
	}

	return certResp, nil
}

// Return the SharedResources from our base stack that are needed for pool
// stacks. We need the IDs for subnets and security groups, since they cannot
// be referenced by name in a VPC. We also lookup the IAM instance profile
// created by the base stack for use in pool's launch configs.  This could be
// cached to disk so that we don't need to lookup the base stack to build a
// pool template.
func GetSharedResources(stackName string) (SharedResources, error) {
	shared := SharedResources{
		SecurityGroups: make(map[string]string),
		Roles:          make(map[string]string),
		Parameters:     make(map[string]string),
		ServerCerts:    make(map[string]string),
	}

	// we need to use DescribeStacks to get any parameters that were used in
	// the base stack, such as KeyName
	descResp, err := DescribeStacks(stackName)
	if err != nil {
		return shared, err
	}

	// load all parameters from the base stack into the shared values
	for _, stack := range descResp.Stacks {
		if stack.Name == stackName {
			for _, param := range stack.Parameters {
				shared.Parameters[param.Key] = param.Value
			}
		}
	}

	res, err := ListStackResources(stackName)
	if err != nil {
		return shared, err
	}

	for _, resource := range res.Resources {
		switch resource.Type {
		case "AWS::EC2::SecurityGroup":
			shared.SecurityGroups[resource.LogicalId] = resource.PhysicalId
		case "AWS::IAM::InstanceProfile":
			shared.Roles[resource.LogicalId] = resource.PhysicalId
		case "AWS::EC2::VPC":
			shared.VPCID = resource.PhysicalId
		}
	}

	// NOTE: using default AZ
	snResp, err := DescribeSubnets(shared.VPCID, "")
	if err != nil {
		return shared, err
	}
	shared.Subnets = snResp.Subnets

	// now we need to find any server certs we may have
	certResp, err := ListServerCertificates()
	if err != nil {
		// we've made it this far, just log this error so we can at least get the CF data
		log.Error("error listing server certificates:", err)
	}

	for _, cert := range certResp.Certs {
		shared.ServerCerts[cert.ServerCertificateName] = cert.Arn
	}

	return shared, nil
}

func GetTemplate(name string) ([]byte, error) {
	svc, err := getService("cf", "")
	if err != nil {
		return nil, err
	}

	params := map[string]string{
		"Action":    "GetTemplate",
		"StackName": name,
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return nil, err
	}
	defer resp.Body.Close()

	tmplResp := GetTemplateResponse{}
	err = xml.NewDecoder(resp.Body).Decode(&tmplResp)

	return tmplResp.TemplateBody, err
}

// Create a CloudFormation stack
// Request parameters which are taken from the options:
//   StackPolicyDuringUpdateBody: optional update policy
//   tag.KEY: tags to be applied to this stack at creation
func Create(name string, stackTmpl []byte, options map[string]string) (*CreateStackResponse, error) {
	svc, err := getService("cf", "")
	if err != nil {
		return nil, err
	}

	params := map[string]string{
		"Action":              "CreateStack",
		"StackName":           name,
		"TemplateBody":        string(stackTmpl),
		"Tags.member.1.Key":   "Name",
		"Tags.member.1.Value": name,
	}

	optNum := 1
	tagNum := 2
	for key, val := range options {
		if key == "StackPolicyDuringUpdateBody" {
			params["StackPolicyDuringUpdateBody"] = val
			continue
		}

		if strings.HasPrefix(strings.ToLower(key), "tag.") {
			params[fmt.Sprintf("Tags.member.%d.Key", tagNum)] = key[4:]
			params[fmt.Sprintf("Tags.member.%d.Value", tagNum)] = val
			tagNum++
			continue
		}

		// everything else goes under Parameters
		params[fmt.Sprintf("Parameters.member.%d.ParameterKey", optNum)] = key
		params[fmt.Sprintf("Parameters.member.%d.ParameterValue", optNum)] = val
		optNum++
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return nil, err
	}
	defer resp.Body.Close()

	createResp := &CreateStackResponse{}
	err = xml.NewDecoder(resp.Body).Decode(createResp)
	if err != nil {
		return nil, err
	}

	return createResp, nil
}

// Update an existing CloudFormation stack.
// Request parameters which are taken from the options:
//   StackPolicyDuringUpdateBody
func Update(name string, stackTmpl []byte, options map[string]string) (*UpdateStackResponse, error) {
	svc, err := getService("cf", "")
	if err != nil {
		return nil, err
	}

	params := map[string]string{
		"Action":       "UpdateStack",
		"StackName":    name,
		"TemplateBody": string(stackTmpl),
	}

	optNum := 1
	for key, val := range options {
		if key == "StackPolicyDuringUpdateBody" {
			params["StackPolicyDuringUpdateBody"] = val
			continue
		}

		if strings.HasPrefix(strings.ToLower(key), "tag.") {
			// Currently can't update a stack's tags
			continue
		}

		params[fmt.Sprintf("Parameters.member.%d.ParameterKey", optNum)] = key
		params[fmt.Sprintf("Parameters.member.%d.ParameterValue", optNum)] = val
		optNum++
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return nil, err
	}
	defer resp.Body.Close()

	updateResp := &UpdateStackResponse{}
	err = xml.NewDecoder(resp.Body).Decode(updateResp)
	if err != nil {
		return nil, err
	}

	return updateResp, nil

}

// Delete and entire stack by name
func Delete(name string) (*DeleteStackResponse, error) {
	svc, err := getService("cf", "")
	if err != nil {
		return nil, err
	}

	params := map[string]string{
		"Action":    "DeleteStack",
		"StackName": name,
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		err := svc.BuildError(resp)
		return nil, err
	}
	defer resp.Body.Close()

	deleteResp := &DeleteStackResponse{}
	err = xml.NewDecoder(resp.Body).Decode(deleteResp)
	if err != nil {
		return nil, err
	}

	return deleteResp, nil
}

// Return a default template to create our base stack.
func DefaultGalaxyTemplate() []byte {
	azResp, err := DescribeAvailabilityZones("")
	if err != nil {
		log.Warn(err)
		return nil
	}

	p := &GalaxyTmplParams{
		Name:    "galaxy",
		VPCCIDR: "10.24.0.1/16",
	}

	for i, az := range azResp.AvailabilityZones {
		s := &SubnetTmplParams{
			Name:   fmt.Sprintf("galaxySubnet%d", i+1),
			Subnet: fmt.Sprintf("10.24.%d.0/24", i+1),
			AZ:     az.Name,
		}

		p.Subnets = append(p.Subnets, s)
	}

	tmpl, err := GalaxyTemplate(p)
	if err != nil {
		// TODO
		log.Fatal(err)
	}
	return tmpl
}

// set a stack policy
// TODO: add delete policy
func SetPolicy(name string, policy []byte) error {
	svc, err := getService("cf", "")
	if err != nil {
		return err
	}

	params := map[string]string{
		"Action":          "SetStackPolicy",
		"StackName":       name,
		"StackPolicyBody": string(policy),
	}

	resp, err := svc.Query("POST", "/", params)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return svc.BuildError(resp)
	}

	return nil
}
