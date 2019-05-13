package preprovision

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/neptune"
	_ "github.com/lib/pq"
	uuid "github.com/nu7hatch/gouuid"
)

type neptuneParams struct {
	DBInstanceClass      string
	Engine               string
	DBInstanceIdentifier string
	MultiAZ              bool
	DBSubnetGroupName    string
	StorageEncrypted     bool
	KmsKeyID             string
	Securitygroupid      string
	Endpoint             string
	Accesskey            string
	Secretkey            string
}

//NeptuneUser ..
type NeptuneUser struct {
	Username  string
	Arn       string
	Accesskey string
	Secretkey string
}

//SimpleUserPolicy ...
type SimpleUserPolicy struct {
	PolicyName string
	Arn        string
}

//UserPolicy ...
type UserPolicy struct {
	Statement []UserPolicyStatement `json:"Statement"`
	Version   string                `json:"Version"`
}

//UserPolicyStatement ...
type UserPolicyStatement struct {
	Resource []string `json:"Resource"`
	Action   []string `json:"Action"`
	Effect   string   `json:"Effect"`
}

var currentTime time.Time

func Run() {

	// initialize time (timezone, etc)
	currentTime = time.Now().UTC()
	location, err := time.LoadLocation("America/Denver")
	if err == nil {
		currentTime = currentTime.In(location)
	} else {
		fmt.Println(err.Error())
	}

	fmt.Println("Neptune Preprovisioner Started at " + currentTime.String())

	small, _ := strconv.Atoi(os.Getenv("PROVISION_SMALL"))
	if need("small", small) {
		record(provision("small"), "small")
	}

	insertEndpoints()

	// Separate output
	fmt.Println("")
}

// Checks to see if there are at least 'minimum' instances of type 'plan' in the database
func need(plan string, minimum int) bool {
	uri := os.Getenv("BROKER_DB")
	db, err := sql.Open("postgres", uri)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	defer db.Close()

	var unclaimedcount int
	err = db.QueryRow("SELECT count(*) as unclaimedcount from provision where plan='" + plan + "' and claimed='no'").Scan(&unclaimedcount)
	if err != nil {
		fmt.Println(err)
		return false
	}

	fmt.Println("Need " + strconv.Itoa(minimum) + " available " + plan + " instances, currently have: " + strconv.Itoa(unclaimedcount))
	if unclaimedcount < minimum {
		return true
	}
	return false
}

func provision(plan string) neptuneParams {
	dbparams := new(neptuneParams)

	if plan == "small" {
		dbparams.DBInstanceClass = "db.r4.large"
	}

	dbparams.Engine = "neptune"

	// DBInstanceIdentifier (uuid + prefix)
	neptuneuuid, _ := uuid.NewV4()
	dbparams.DBInstanceIdentifier = os.Getenv("NAME_PREFIX") + strings.Split(neptuneuuid.String(), "-")[0]
	fmt.Println(dbparams.DBInstanceIdentifier)

	dbparams.MultiAZ = false
	dbparams.DBSubnetGroupName = os.Getenv("SUBNET_GROUP_NAME")
	dbparams.StorageEncrypted = true
	dbparams.KmsKeyID = os.Getenv("KMS_KEY_ID")
	dbparams.Securitygroupid = os.Getenv("SECURITY_GROUP_ID")

	svc := neptune.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	clusterParams := &neptune.CreateDBClusterInput{
		Engine:                          aws.String(dbparams.Engine),
		DBClusterIdentifier:             aws.String(dbparams.DBInstanceIdentifier),
		DBSubnetGroupName:               aws.String(dbparams.DBSubnetGroupName),
		StorageEncrypted:                aws.Bool(dbparams.StorageEncrypted),
		EnableIAMDatabaseAuthentication: aws.Bool(true),
		VpcSecurityGroupIds: []*string{
			aws.String(dbparams.Securitygroupid),
		},
	}

	instanceParams := &neptune.CreateDBInstanceInput{
		DBInstanceClass:      aws.String(dbparams.DBInstanceClass),
		DBInstanceIdentifier: aws.String(dbparams.DBInstanceIdentifier),
		Engine:               aws.String(dbparams.Engine),
		DBClusterIdentifier:  aws.String(dbparams.DBInstanceIdentifier),
		DBSubnetGroupName:    aws.String(dbparams.DBSubnetGroupName),
		Tags: []*neptune.Tag{
			{
				Key:   aws.String("Name"),
				Value: aws.String(dbparams.DBInstanceIdentifier),
			},
		},
		StorageEncrypted: aws.Bool(dbparams.StorageEncrypted),
	}

	resp, err := svc.CreateDBCluster(clusterParams)
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(2)
	}
	fmt.Println(resp)

	resp2, err2 := svc.CreateDBInstance(instanceParams)
	if err2 != nil {
		fmt.Println(err2.Error())
		os.Exit(2)
	}
	fmt.Println(resp2)

	// Setup IAM Authentication
	neptuneUser := createUser(*instanceParams.DBInstanceIdentifier)
	simpleuserpolicy := createUserPolicy(*instanceParams.DBInstanceIdentifier, *resp.DBCluster.DbClusterResourceId)
	attachUserPolicy(*instanceParams.DBInstanceIdentifier, simpleuserpolicy)

	dbparams.Accesskey = neptuneUser.Accesskey
	dbparams.Secretkey = neptuneUser.Secretkey

	return *dbparams
}

func record(dbparams neptuneParams, plan string) {

	uri := os.Getenv("BROKER_DB")
	db, err := sql.Open("postgres", uri)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	defer db.Close()

	var newname string
	err = db.QueryRow("INSERT INTO provision(name,plan,claimed,makeDate,billingcode,endpoint, accesskey, secretkey) VALUES($1,$2,$3,$4,$5,$6,$7,$8) returning name;", dbparams.DBInstanceIdentifier, plan, "no", currentTime.Format("2006-01-02 15:04:05"), "preprovisioned", dbparams.Endpoint, dbparams.Accesskey, dbparams.Secretkey).Scan(&newname)

	if err != nil {
		fmt.Println(err)
		return
	}
	fmt.Println(newname)
}

func insertEndpoints() {
	uri := os.Getenv("BROKER_DB")
	db, err := sql.Open("postgres", uri)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	defer db.Close()

	rows, err := db.Query("select name from provision where endpoint=''")
	if err != nil {
		fmt.Println(err)
		return
	}

	fmt.Println("Looking for endpoints...")

	for rows.Next() {
		var name string
		err = rows.Scan(&name)
		if err != nil {
			fmt.Println(err)
			return
		}

		fmt.Println("Attempting add endpoint for " + name + "...")
		if isAvailable(name) {
			endpoint, eerr := getEndpoint(name)
			if eerr != nil {
				fmt.Println(err)
				return
			}
			addEndpoint(name, endpoint)
		}
	}
}

func addEndpoint(name string, endpoint string) {
	uri := os.Getenv("BROKER_DB")
	db, err := sql.Open("postgres", uri)
	if err != nil {
		fmt.Println(err)
		os.Exit(2)
	}
	defer db.Close()

	_, err = db.Exec("UPDATE provision SET endpoint=$1 WHERE name=$2", endpoint, name)
	if err != nil {
		fmt.Println(err)
		return
	}
}

func getEndpoint(name string) (endpoint string, err error) {
	region := os.Getenv("REGION")
	svc := neptune.New(session.New(&aws.Config{
		Region: aws.String(region),
	}))
	params := &neptune.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(name),
		MaxRecords:           aws.Int64(20),
	}
	resp, err := svc.DescribeDBInstances(params)
	if err != nil {
		fmt.Println(err)
		err = errors.New("Failed to get instance information for " + name)
		return endpoint, err
	}

	endpoint = *resp.DBInstances[0].Endpoint.Address + ":" + strconv.FormatInt(*resp.DBInstances[0].Endpoint.Port, 10)
	return endpoint, nil
}

func isAvailable(name string) bool {
	region := os.Getenv("REGION")

	svc := neptune.New(session.New(&aws.Config{
		Region: aws.String(region),
	}))

	rparams := &neptune.DescribeDBInstancesInput{
		DBInstanceIdentifier: aws.String(name),
		MaxRecords:           aws.Int64(20),
	}
	rresp, rerr := svc.DescribeDBInstances(rparams)
	if rerr != nil {
		fmt.Println(rerr)
		return false
	}
	fmt.Println("Checking to see if available...")
	fmt.Println(name + " Status: " + *rresp.DBInstances[0].DBInstanceStatus)
	status := *rresp.DBInstances[0].DBInstanceStatus
	if status == "available" {
		return true
	}
	return false
}

// IAM Helper Functions
func createUser(username string) NeptuneUser {

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.CreateUserInput{
		UserName: aws.String(username),
	}
	resp, err := svc.CreateUser(params)

	if err != nil {
		fmt.Println(err.Error())

	}

	arn := *resp.User.Arn

	paramskey := &iam.CreateAccessKeyInput{
		UserName: aws.String(username),
	}
	respkey, err := svc.CreateAccessKey(paramskey)

	if err != nil {
		fmt.Println(err.Error())
	}

	accesskey := *respkey.AccessKey.AccessKeyId
	secretkey := *respkey.AccessKey.SecretAccessKey
	var neptuneuser NeptuneUser
	neptuneuser.Username = username
	neptuneuser.Arn = arn
	neptuneuser.Accesskey = accesskey
	neptuneuser.Secretkey = secretkey
	return neptuneuser

}

func createUserPolicy(username string, resourceID string) SimpleUserPolicy {

	var userpolicy UserPolicy
	userpolicy.Version = "2012-10-17"
	var statements []UserPolicyStatement
	var statement UserPolicyStatement
	statement.Effect = "Allow"
	var resources []string
	resources = append(resources, "arn:aws:neptune-db:"+os.Getenv("REGION")+":"+os.Getenv("ACCOUNTNUMBER")+":"+resourceID+"/*")
	resources = append(resources, "arn:aws:neptune-db:"+os.Getenv("REGION")+":"+os.Getenv("ACCOUNTNUMBER")+":"+resourceID)
	statement.Resource = resources
	var actions []string
	actions = append(actions, "neptune-db:*")
	statement.Action = actions
	statements = append(statements, statement)
	userpolicy.Statement = statements
	str, err := json.Marshal(userpolicy)
	if err != nil {
		fmt.Println("Error preparing request")
	}
	jsonStr := (string(str))

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.CreatePolicyInput{
		PolicyDocument: aws.String(jsonStr),
		PolicyName:     aws.String(username + "policy"),
	}
	resp, err := svc.CreatePolicy(params)

	if err != nil {
		fmt.Println(err.Error())
	}

	policyarn := *resp.Policy.Arn
	policyname := *resp.Policy.PolicyName
	var simpleuserpolicy SimpleUserPolicy
	simpleuserpolicy.PolicyName = policyname
	simpleuserpolicy.Arn = policyarn
	return simpleuserpolicy
}

func attachUserPolicy(username string, simpleuserpolicy SimpleUserPolicy) {
	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.AttachUserPolicyInput{
		PolicyArn: aws.String(simpleuserpolicy.Arn),
		UserName:  aws.String(username),
	}
	_, err := svc.AttachUserPolicy(params)

	if err != nil {
		fmt.Println(err.Error())
		return
	}

}
