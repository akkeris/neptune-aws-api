# AWS Neptune Broker for Akkeris
Create and delete AWS Neptune graph databases

## Usage
``` 
go build neptune.go
neptune [api | preprovision]
```
`api` -  Runs the REST API for claiming and deleting Neptune instances

`preprovision` -  Starts the preprovisioner, which runs every minute and makes sure that there are always the specified number of unclaimed Neptune instances

## Details

### API Endpoints

| Method | Endpoint                   | Description                                                                             |
|--------|----------------------------|-----------------------------------------------------------------------------------------|
| GET    | /v1/neptune/plans          | Get list of available instance plans                                                    |
| GET    | /v1/neptune/url/:name      | Get endpoint, access key, secret key, and region of an instance                         |
| POST   | /v1/neptune/instance       | Claim preprovisioned instance -  {"plan":"small", "billingcode":"department"}           |
| POST   | /v1/neptune/tag            | Tag a preprovisioned instance -  {"resource":"name", "name":"key", "value":"value"}     |
| DELETE | /v1/neptune/instance/:name | Delete a preprovisioned instance                                                        |

See below for examples.

## Dependencies
1. "database/sql"
2. "encoding/json"
3. "errors"
4. "fmt"
5. "log"
6. "os"
7. "strconv"
8. "strings"
9. "time"
10. "github.com/aws/aws-sdk-go/aws"
11. "github.com/aws/aws-sdk-go/service/iam"
12. "github.com/aws/aws-sdk-go/service/neptune"
13. "github.com/aws/aws-sdk-go/aws/session"
14. "github.com/robfig/cron"
15. "github.com/nu7hatch/gouuid"
16. "github.com/go-martini/martini"
17. "github.com/martini-contrib/binding"
18. "github.com/martini-contrib/render"
19. "github.com/lib/pq"

## Requirements
go

AWS Credentials

## Runtime Environment Variables

Shared:
- ACCOUNTNUMBER - AWS account number
- BROKER_DB - Postgres database, e.g. `postgres://[usr]:[pwd]@[url]:[port]/[db_name]`
- REGION - AWS region

Preprovisioner:
- KMS_KEY_ID - AWS KMS key ID for encryption
- NAME_PREFIX
- PROVISION_SMALL - Number of small (db.r4.large) instances to preprovision
- SECURITY_GROUP_ID - AWS VPC security group
- SUBNET_GROUP_NAME - RDS subnet
- RUN_AS_CRON - (optional) if supplied, will create a cron job to run every minute

API:
- PORT - (optional) port to listen on, default 3000

## Examples

`curl hostname:3000/v1/neptune/plans`

Response: `{ "small": "Small DB Instance - 2vCPU, 15.25 GiB RAM - $245/mo" }`

&nbsp;

`curl hostname:3000/v1/neptune/url/name`

Response: 
```
{ 
  "NEPTUNE_DATABASE_URL": "name.id.region.neptune.amazonaws.com:8182",
  "NEPTUNE_ACCESS_KEY": "ACC3SSK3Y",
  "NEPTUNE_SECRET_KEY": "sEcReTkEy",
  "NEPTUNE_REGION": "us-west-2",
}
```

&nbsp;

`curl hostname:3000/v1/neptune/instance -X POST -d '{ "plan": "small", "billingcode": "department" }'`

Response:
```
{ 
  "NEPTUNE_DATABASE_URL": "name.id.region.neptune.amazonaws.com:8182",
  "NEPTUNE_ACCESS_KEY": "ACC3SSK3Y",
  "NEPTUNE_SECRET_KEY": "sEcReTkEy",
  "NEPTUNE_REGION": "us-west-2",
}
```

&nbsp;

`curl hostname:3000/v1/neptune/tag -x POST -d '{ "resource": "name", "name": "key", "value": "value" }`

Response: `{ "Response": "Tag added" }`

&nbsp;

`curl hostname:3000/v1/neptune/instance/name -X DELETE`

Response `{ "Response": "Instance deletion in progress" }`
