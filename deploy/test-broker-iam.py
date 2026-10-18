"""Exercise actual MiniStack authorization, including negative cases."""
import configparser
import uuid
import boto3
from botocore.exceptions import ClientError

c = configparser.ConfigParser()
c.read("/credentials/credentials")
def client(profile, bad_secret=False):
    return boto3.client("sqs", endpoint_url="http://ministack:4566", region_name="us-east-1",
        aws_access_key_id=c[profile]["aws_access_key_id"],
        aws_secret_access_key="wrong-secret" if bad_secret else c[profile]["aws_secret_access_key"])
root = boto3.client("sqs", endpoint_url="http://ministack:4566", region_name="us-east-1")
incoming = root.get_queue_url(QueueName="wager-transactions.fifo")["QueueUrl"]
outgoing = root.get_queue_url(QueueName="wallet-events.fifo")["QueueUrl"]
def denied(label, call):
    try: call()
    except ClientError as exc:
        if exc.response["ResponseMetadata"]["HTTPStatusCode"] not in (400,401,403): raise
        if exc.response["Error"]["Code"] not in ("AccessDenied","AccessDeniedException","InvalidClientTokenId","SignatureDoesNotMatch","InvalidAccessKeyId","UnrecognizedClientException"):
            raise
        print("PASS denied:",label)
    else: raise AssertionError("Unexpectedly authorized: "+label)
ingress, service, reader = map(client,["trusted-ingress","wallet-service","event-reader"])
ingress.get_queue_url(QueueName="wager-transactions.fifo")
service.get_queue_attributes(QueueUrl=incoming,AttributeNames=["QueueArn"])
reader.get_queue_url(QueueName="wallet-events.fifo")
denied("ingress reads incoming",lambda:ingress.receive_message(QueueUrl=incoming))
denied("ingress sends output",lambda:ingress.send_message(QueueUrl=outgoing,MessageBody="{}",MessageGroupId="iam-test",MessageDeduplicationId=str(uuid.uuid4())))
denied("service sends incoming",lambda:service.send_message(QueueUrl=incoming,MessageBody="{}",MessageGroupId="iam-test",MessageDeduplicationId=str(uuid.uuid4())))
denied("reader sends incoming",lambda:reader.send_message(QueueUrl=incoming,MessageBody="{}",MessageGroupId="iam-test",MessageDeduplicationId=str(uuid.uuid4())))
unknown=boto3.client("sqs",endpoint_url="http://ministack:4566",region_name="us-east-1",aws_access_key_id="unknown-key",aws_secret_access_key="unknown-secret")
denied("unknown credentials",lambda:unknown.get_queue_attributes(QueueUrl=incoming,AttributeNames=["QueueArn"]))
denied("wrong signature",lambda:client("trusted-ingress",True).get_queue_url(QueueName="wager-transactions.fifo"))
print("PASS: permitted calls and IAM negative cases verified against real broker.")
