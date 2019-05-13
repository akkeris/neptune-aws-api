package main

import (
	"database/sql"
	"errors"
	"fmt"
	"os"

	api "neptune-aws-api/api"
	preprovision "neptune-aws-api/preprovision"

	_ "github.com/lib/pq"
	"github.com/robfig/cron"
)

func main() {
	if len(os.Args) != 2 || (os.Args[1] != "preprovision" && os.Args[1] != "api") {
		fmt.Println("Usage: neptune [preprovision | api]")
		fmt.Println("   api: Run neptune REST API")
		fmt.Println("   preprovision: Run neptune preprovisioner")
		os.Exit(1)
	}

	err := checkEnvironmentVariables(os.Args[1])
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	err = initDB()
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	if os.Args[1] == "preprovision" {
		fmt.Println("Running in Preprovision Mode...")
		fmt.Println("")

		if os.Getenv("RUN_AS_CRON") != "" {
			fmt.Println("Running as cron job...")
			fmt.Println("")
			c := cron.New()
			c.AddFunc("@every 1m", preprovision.Run)
			c.Run()
		} else {
			preprovision.Run()
		}

	} else if os.Args[1] == "api" {
		fmt.Println("Running in API Mode...")
		fmt.Println("")
		api.Run()
	}
}

func checkEnvironmentVariables(mode string) error {

	if os.Getenv("REGION") == "" {
		return errors.New("Missing REGION environment variable")
	}
	if os.Getenv("BROKER_DB") == "" {
		return errors.New("Missing BROKER_DB environment variable")
	}
	if os.Getenv("ACCOUNTNUMBER") == "" {
		return errors.New("Missing ACCOUNTNUMBER environment variable")
	}

	if mode == "preprovision" {
		if os.Getenv("PROVISION_SMALL") == "" {
			return errors.New("Missing PROVISION_SMALL environment variable")
		}
		if os.Getenv("NAME_PREFIX") == "" {
			return errors.New("Missing NAME_PREFIX environment variable")
		}
		if os.Getenv("SECURITY_GROUP_ID") == "" {
			return errors.New("Missing SECURITY_GROUP_ID environment variable")
		}
		if os.Getenv("SUBNET_GROUP_NAME") == "" {
			return errors.New("Missing SUBNET_GROUP_NAME environment variable")
		}
		if os.Getenv("KMS_KEY_ID") == "" {
			return errors.New("Missing KMS_KEY_ID environment variable")
		}
	}

	return nil
}

func initDB() error {
	uri := os.Getenv("BROKER_DB")
	db, err := sql.Open("postgres", uri)
	if err != nil {
		return errors.New("Unable to establish database connection: " + err.Error())
	}
	defer db.Close()

	createStmt := `
		CREATE TABLE if not exists provision (
    	name character varying(200) PRIMARY KEY,
    	plan character varying(200),
    	claimed character varying(200),
    	makeDate timestamp without time zone DEFAULT now(),
    	billingcode character varying(200),
    	endpoint character varying(200),
    	accesskey character varying(200),
    	secretkey character varying(200)
		);
		
		CREATE UNIQUE INDEX if not exists name_pkey ON provision(name text_ops);`

	_, err = db.Exec(createStmt)
	if err != nil {
		return errors.New("Unable to create database: " + err.Error())
	}
	return nil
}
