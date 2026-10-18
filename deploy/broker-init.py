"""Provision local-only IAM identities; never print credentials."""
import configparser
import json
import os
from pathlib import Path
import boto3
from botocore.exceptions import ClientError

session = boto3.Session(region_name="us-east-1")
iam = session.client("iam", endpoint_url="http://ministack:4566")
account = session.client("sts", endpoint_url="http://ministack:4566").get_caller_identity()["Account"]
arn = lambda name: f"arn:aws:sqs:us-east-1:{account}:{name}"
incoming, dead, outgoing = map(arn, ["wager-transactions.fifo", "wager-transactions-dlq.fifo", "wallet-events.fifo"])
policies = {
 "wallet-service": [
  {"Effect":"Allow","Action":["sqs:CreateQueue","sqs:GetQueueUrl","sqs:GetQueueAttributes"],"Resource":[incoming,dead,outgoing]},
  {"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility"],"Resource":incoming},
  {"Effect":"Allow","Action":"sqs:SendMessage","Resource":[dead,outgoing]}],
 "trusted-ingress": [{"Effect":"Allow","Action":["sqs:GetQueueUrl","sqs:SendMessage"],"Resource":incoming}],
 "event-reader": [{"Effect":"Allow","Action":["sqs:GetQueueUrl","sqs:ReceiveMessage","sqs:DeleteMessage","sqs:ChangeMessageVisibility"],"Resource":outgoing}],
}
path = Path("/credentials/credentials")
credentials = configparser.ConfigParser()
if path.exists(): credentials.read(path)
for user, statements in policies.items():
 try: iam.create_user(UserName=user)
 except ClientError as exc:
  if exc.response["Error"]["Code"] != "EntityAlreadyExists": raise
 iam.put_user_policy(UserName=user, PolicyName="wallet-local", PolicyDocument=json.dumps({"Version":"2012-10-17","Statement":statements}))
 keys = iam.list_access_keys(UserName=user)["AccessKeyMetadata"]
 saved = credentials.get(user,"aws_access_key_id",fallback="")
 if not any(k["AccessKeyId"] == saved and k["Status"] == "Active" for k in keys):
  for key in keys: iam.delete_access_key(UserName=user, AccessKeyId=key["AccessKeyId"])
  key = iam.create_access_key(UserName=user)["AccessKey"]
  credentials[user] = {"aws_access_key_id":key["AccessKeyId"],"aws_secret_access_key":key["SecretAccessKey"]}
temporary = path.with_suffix(".tmp")
with temporary.open("w") as stream: credentials.write(stream)
os.chmod(temporary,0o644)
temporary.replace(path)
print("Provisioned three local IAM identities and policies.")
