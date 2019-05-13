package api

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/iam"
	"github.com/aws/aws-sdk-go/service/neptune"
	"github.com/go-martini/martini"
	_ "github.com/lib/pq"
	"github.com/martini-contrib/binding"
	"github.com/martini-contrib/render"
)

type provisionspec struct {
	Plan        string `json:"plan"`
	Billingcode string `json:"billingcode"`
}
type tagspec struct {
	Resource string `json:"resource"`
	Name     string `json:"name"`
	Value    string `json:"value"`
}
type dbspec struct {
	AccessKeyID     string
	SecretAccessKey string
	Endpoint        string
}

var pool *sql.DB
var plans map[string]interface{}

// TODO: what error should we display if the accesskey/secretkey is not in the DB?
// TODO: What if instance/cluster exists in database but has been deleted in AWS (for DELETE, GET)?
// TODO: What if user, policy DNE in AWS when we try to delete the instance?

// Run - starts the API
func Run() {
	pool = setupDB()

	// Create plans
	plans = make(map[string]interface{})
	plans["small"] = "Small DB Instance - 2vCPU, 15.25 GiB RAM - $245/mo"

	m := martini.Classic()
	m.Use(render.Renderer())

	m.Post("/v1/neptune/instance", binding.Json(provisionspec{}), claimInstance)
	m.Delete("/v1/neptune/instance/:name", deleteInstance)
	m.Get("/v1/neptune/url/:name", getInstance)
	m.Get("/v1/neptune/plans", func(r render.Render) { r.JSON(200, plans) })
	m.Post("/v1/neptune/tag", binding.Json(tagspec{}), tagInstance)

	m.Run()
}

// Mark a specified instance as 'claimed' and send the instance's endpoint as a response
func claimInstance(spec provisionspec, err binding.Errors, r render.Render) {
	var name string

	//Bad JSON
	if spec.Plan == "" || spec.Billingcode == "" {
		fmt.Println("Invalid JSON")
		r.Text(400, "Bad Request")
		return
	}

	_, ok := plans[spec.Plan]
	if !ok {
		fmt.Println("Invalid plan")
		r.Text(400, "Bad Request")
		return
	}

	dberr := pool.QueryRow("SELECT name FROM provision WHERE plan=$1 AND claimed='no' AND makedate=(SELECT min(makedate) FROM provision WHERE plan=$1 AND claimed='no')", spec.Plan).Scan(&name)
	if dberr != nil && dberr.Error() == "sql: no rows in result set" {
		fmt.Println("No available instances")
		r.JSON(503, map[string]string{"error": "No available instances. Try again in 10 minutes"})
		return
	} else if dberr != nil {
		output500Error(r, dberr)
		return
	}

	fmt.Println("Claiming " + name + "...")

	available := isAvailable(name)
	if available {
		_, dberr = pool.Exec("UPDATE provision SET claimed='yes', billingcode=$1 WHERE name=$2", spec.Billingcode, name)
		if dberr != nil {
			output500Error(r, dberr)
			return
		}

		region := os.Getenv("REGION")
		svc := neptune.New(session.New(&aws.Config{
			Region: aws.String(region),
		}))
		accountnumber := os.Getenv("ACCOUNTNUMBER")
		clusterarn := "arn:aws:rds:" + region + ":" + accountnumber + ":cluster:" + name
		instancearn := "arn:aws:rds:" + region + ":" + accountnumber + ":db:" + name

		clusterParams := &neptune.AddTagsToResourceInput{
			ResourceName: aws.String(clusterarn),
			Tags: []*neptune.Tag{ // Required
				{
					Key:   aws.String("billingcode"),
					Value: aws.String(spec.Billingcode),
				},
			},
		}

		_, awserr := svc.AddTagsToResource(clusterParams)
		if awserr != nil {
			output500Error(r, awserr)
			return
		}

		instanceParams := &neptune.AddTagsToResourceInput{
			ResourceName: aws.String(instancearn),
			Tags: []*neptune.Tag{ // Required
				{
					Key:   aws.String("billingcode"),
					Value: aws.String(spec.Billingcode),
				},
			},
		}

		_, awserr = svc.AddTagsToResource(instanceParams)
		if awserr != nil {
			output500Error(r, awserr)
			return
		}

		dbinfo, err := getDBInfo(name)
		if err != nil {
			output500Error(r, err)
		}
		r.JSON(200, map[string]string{"NEPTUNE_DATABASE_URL": dbinfo.Endpoint, "NEPTUNE_ACCESS_KEY": dbinfo.AccessKeyID, "NEPTUNE_SECRET_KEY": dbinfo.SecretAccessKey, "NEPTUNE_REGION": os.Getenv("REGION")})
	} else {
		r.JSON(503, map[string]string{"error": "No available instances. Try again in 10 minutes"})
		return
	}

}

// Delete a specified instance and remove its row from the database
func deleteInstance(params martini.Params, r render.Render) {
	instanceName := params["name"]

	if !instanceExists(instanceName) {
		fmt.Println("Instance with specified name does not exist in provision table")
		r.Text(400, "Bad Request")
		return
	}

	svc := neptune.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	instanceParamsDelete := &neptune.DeleteDBInstanceInput{
		DBInstanceIdentifier: aws.String(instanceName),
		SkipFinalSnapshot:    aws.Bool(true),
	}

	clusterParamsDelete := &neptune.DeleteDBClusterInput{
		DBClusterIdentifier: aws.String(instanceName),
		SkipFinalSnapshot:   aws.Bool(true),
	}

	instanceResp, instanceErr := svc.DeleteDBInstance(instanceParamsDelete)
	name := *instanceParamsDelete.DBInstanceIdentifier
	if instanceErr != nil {
		fmt.Println(instanceErr.Error())
		output500Error(r, instanceErr)
		return
	}
	fmt.Println("Deletion in progress for instance " + *instanceResp.DBInstance.DBInstanceIdentifier)

	clusterResp, clusterErr := svc.DeleteDBCluster(clusterParamsDelete)
	if clusterErr != nil {
		fmt.Println(instanceErr.Error())
		output500Error(r, clusterErr)
		return
	}
	fmt.Println("Deletion in progress for cluster " + *clusterResp.DBCluster.DBClusterIdentifier)

	_, err := pool.Exec("DELETE FROM provision WHERE name=$1", name)
	if err != nil {
		fmt.Println(err.Error())
		output500Error(r, err)
		return
	}

	r.JSON(200, map[string]string{"Response": "Instance deletion in progress"})

	username := *instanceParamsDelete.DBInstanceIdentifier
	deleteUserPolicy(username)
	deleteAccessKey(username)
	deleteUser(username)

}

// Send the endpoint of a specified instance as a response
func getInstance(params martini.Params, r render.Render) {
	name := params["name"]

	if !instanceExists(name) {
		fmt.Println("Instance with specified name does not exist in provision table")
		r.Text(400, "Bad Request")
		return
	}

	dbinfo, err := getDBInfo(name)
	if err != nil {
		output500Error(r, err)
		return
	}
	r.JSON(200, map[string]string{"NEPTUNE_DATABASE_URL": dbinfo.Endpoint, "NEPTUNE_ACCESS_KEY": dbinfo.AccessKeyID, "NEPTUNE_SECRET_KEY": dbinfo.SecretAccessKey, "NEPTUNE_REGION": os.Getenv("REGION")})
}

// Tag a specified instance with the provided name and value
func tagInstance(spec tagspec, berr binding.Errors, r render.Render) {
	if berr != nil {
		fmt.Println(berr)
		r.Text(400, "Bad Request")
		return
	}

	//Bad JSON
	if spec.Resource == "" || spec.Name == "" || spec.Value == "" {
		fmt.Println("Invalid JSON")
		r.Text(400, "Bad Request")
		return
	}

	if !instanceExists(spec.Resource) {
		fmt.Println("Instance " + spec.Name + " does not exist in provision table")
		r.Text(400, "Bad Request")
		return
	}

	region := os.Getenv("REGION")
	svc := neptune.New(session.New(&aws.Config{
		Region: aws.String(region),
	}))

	accountnumber := os.Getenv("ACCOUNTNUMBER")
	clusterarn := "arn:aws:rds:" + region + ":" + accountnumber + ":cluster:" + spec.Resource
	instancearn := "arn:aws:rds:" + region + ":" + accountnumber + ":db:" + spec.Resource

	clusterParams := &neptune.AddTagsToResourceInput{
		ResourceName: aws.String(clusterarn),
		Tags: []*neptune.Tag{
			{
				Key:   aws.String(spec.Name),
				Value: aws.String(spec.Value),
			},
		},
	}

	_, awserr := svc.AddTagsToResource(clusterParams)
	if awserr != nil {
		output500Error(r, awserr)
		return
	}

	instanceParams := &neptune.AddTagsToResourceInput{
		ResourceName: aws.String(instancearn),
		Tags: []*neptune.Tag{
			{
				Key:   aws.String(spec.Name),
				Value: aws.String(spec.Value),
			},
		},
	}

	_, awserr = svc.AddTagsToResource(instanceParams)
	if awserr != nil {
		output500Error(r, awserr)
		return
	}

	fmt.Println("Successfully tagged " + spec.Resource + " with '" + spec.Name + "':'" + spec.Value + "'")

	r.JSON(200, map[string]interface{}{"Response": "Tag added"})
}

// IAM Helper Functions

// Detach policy from user and delete it from AWS
func deleteUserPolicy(neptuneName string) {

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	policyarn := getPolicyARN(neptuneName)
	deparams := &iam.DetachUserPolicyInput{
		PolicyArn: aws.String(policyarn),   // Required
		UserName:  aws.String(neptuneName), // Required
	}

	_, err := svc.DetachUserPolicy(deparams)

	if err != nil {
		fmt.Println(err.Error())
		return
	}

	svc = iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.DeletePolicyInput{
		PolicyArn: aws.String(policyarn), // Required
	}
	_, err = svc.DeletePolicy(params)

	if err != nil {
		fmt.Println(err.Error())
		return
	}

}

func getPolicyARN(neptuneName string) string {

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))
	params := &iam.ListAttachedUserPoliciesInput{
		UserName: aws.String(neptuneName), // Required
	}
	resp, err := svc.ListAttachedUserPolicies(params)

	if err != nil {
		fmt.Println(err.Error())
	}

	policyarn := *resp.AttachedPolicies[0].PolicyArn
	return policyarn
}

func deleteUser(neptuneName string) {

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.DeleteUserInput{
		UserName: aws.String(neptuneName), // Required
	}
	_, err := svc.DeleteUser(params)

	if err != nil {
		fmt.Println(err.Error())
		return
	}

}

func deleteAccessKey(neptuneName string) {
	accesskeyid := getAccessKeyID(neptuneName)

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.DeleteAccessKeyInput{
		AccessKeyId: aws.String(accesskeyid), // Required
		UserName:    aws.String(neptuneName),
	}
	_, err := svc.DeleteAccessKey(params)

	if err != nil {
		fmt.Println(err.Error())
		return
	}

}

func getAccessKeyID(neptuneName string) string {

	svc := iam.New(session.New(&aws.Config{
		Region: aws.String(os.Getenv("REGION")),
	}))

	params := &iam.ListAccessKeysInput{
		UserName: aws.String(neptuneName),
	}
	resp, err := svc.ListAccessKeys(params)

	if err != nil {
		fmt.Println(err.Error())
	}

	accesskeyid := *resp.AccessKeyMetadata[0].AccessKeyId
	return accesskeyid

}

// Helper Functions

// Returns whether or not an instance is finished being created
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
	}

	fmt.Println("Checking to see if " + name + " is available...")
	fmt.Println("Current Status: " + *rresp.DBInstances[0].DBInstanceStatus)
	status := *rresp.DBInstances[0].DBInstanceStatus
	if status == "available" {
		return true
	}
	return false
}

// Outputs a 500 error as a response and to the console
func output500Error(r render.Render, err error) {
	fmt.Println(err)
	r.JSON(500, map[string]interface{}{"error": err.Error()})
}

// Queries the database to provide information on an instance
// Currently returns endpoint, will be expanded in the future to username and password
func getDBInfo(name string) (dbinfo dbspec, err error) {
	dbinfo.Endpoint = queryDB("endpoint", name)
	if dbinfo.Endpoint == "" {
		return dbinfo, errors.New("Endpoint not available, try again in a few minutes")
	}

	dbinfo.AccessKeyID = queryDB("accesskey", name)
	dbinfo.SecretAccessKey = queryDB("secretkey", name)
	if dbinfo.AccessKeyID == "" || dbinfo.SecretAccessKey == "" {
		return dbinfo, errors.New("Internal Server Error")
	}

	return dbinfo, nil
}

// Queries the database about a specific column of an instance
func queryDB(i string, name string) string {
	dberr := pool.QueryRow("select " + i + " from provision where name ='" + name + "'").Scan(&i)
	if dberr != nil {
		fmt.Println(dberr.Error())
		return ""
	}
	return i
}

// Initialize the database table as well as create a connection pool
func setupDB() *sql.DB {
	uri := os.Getenv("BROKER_DB")
	db, err := sql.Open("postgres", uri)
	if err != nil {
		fmt.Println(err)
		log.Fatal("Unable to establish database connection.", err)
	}

	hour, _ := time.ParseDuration("1h")
	db.SetConnMaxLifetime(hour)
	db.SetMaxIdleConns(4)
	db.SetMaxOpenConns(20)
	return db
}

// Connect to the database and see if name exists in the provision table
func instanceExists(name string) bool {
	var exists bool
	err := pool.QueryRow("SELECT EXISTS (SELECT FROM PROVISION WHERE name = $1)", name).Scan(&exists)
	if err != nil {
		fmt.Println(err)
		return false
	}
	return exists
}
