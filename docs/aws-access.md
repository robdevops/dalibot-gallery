# AWS access

The gallery reads the bot's DynamoDB table and never writes to it. Give it
its own read-only identity. Do not reuse the bot's user or Lambda role: the
bot's policy can write, and it has no `Scan` or stream access.

None of this is needed for a self-hosted bot that keeps its records in
SQLite: with `GALLERY_SOURCE=sqlite` the gallery reads no AWS setting. See
[SQLite](sqlite.md).

Contents: [The policy](#the-policy) · [Enable the stream](#enable-the-table-stream) · [AWS console](#aws-console) ·
[AWS CLI](#aws-cli) · [Give the key to the gallery](#give-the-key-to-the-gallery) ·
[Running on AWS](#running-on-aws) · [Cost](#cost) · [Troubleshooting](#troubleshooting)

## The policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadTable",
      "Effect": "Allow",
      "Action": ["dynamodb:Scan", "dynamodb:DescribeTable"],
      "Resource": "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dalibot"
    },
    {
      "Sid": "ReadTableStream",
      "Effect": "Allow",
      "Action": ["dynamodb:DescribeStream", "dynamodb:GetShardIterator", "dynamodb:GetRecords"],
      "Resource": "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dalibot/stream/*"
    }
  ]
}
```

Before you use it, edit the two `Resource` lines:

- `REGION`: the table's region. The gallery assumes `ap-southeast-1` unless `AWS_REGION` says otherwise.
- `ACCOUNT_ID`: your 12-digit AWS account number, shown in the menu at the
  top right of the console, or printed by
  `aws sts get-caller-identity --query Account --output text`.
- `dalibot`: the table name, if the bot uses another one (`DALIBOT_TABLE`).

Everything in it only reads. Add `kms:Decrypt` on the key only if the table
is encrypted with a customer-managed KMS key.

## Enable the table stream

The gallery reads the whole table once, and after that only what has changed,
from the table's DynamoDB stream. Without a stream it has to scan the whole
table at every refresh, and a scan is billed for every item it reads (see
[Cost](#cost)). It still works that way, and says so in its log, but turn the
stream on. It does not change anything for the bot. Do this as someone who may
manage the table, once.

**Console.** Open **DynamoDB → Tables → `dalibot`**, the **Exports and
streams** tab, and under **DynamoDB stream details** choose **Turn on**. Pick
the view type **New image** and choose **Turn on stream**.

**CLI:**

```sh
aws dynamodb update-table --table-name dalibot --region ap-southeast-1 \
  --stream-specification StreamEnabled=true,StreamViewType=NEW_IMAGE
```

If the bot's table is managed by CloudFormation, Terraform or a deploy script,
make the change there, or the next deploy turns it off again. In CloudFormation
it is `StreamSpecification: { StreamViewType: NEW_IMAGE }` on the table.

Why **New image**: an added or changed item then arrives whole, so the gallery
makes no further request for it; a removed item (a `#delete`, or the TTL
expiring it) arrives as its key, which is all that is needed. The view type
`KEYS_ONLY` or `OLD_IMAGE` cannot be used; `NEW_AND_OLD_IMAGES` can.

The gallery finds the stream by itself (that is what `dynamodb:DescribeTable`
in the policy is for), so there is nothing to set. How it uses it:

- It reads the whole table once, then reads the stream from its start, which
  holds the last 24 hours of changes. Replaying them is harmless: each change
  replaces the item whole, so the picture ends up as the table is, whatever
  the scan missed or caught.
- It does this at every start (so a restart never loses anything), and again
  every 6 hours as a check. In between, each refresh is one `DescribeStream`
  and a `GetRecords` call or two for each shard, which is a few small requests
  a minute.
- A request that fails in a way that may pass, such as throttling, keeps what
  the gallery has and is tried again at the next refresh. After three failures
  in a row, or at once for something that cannot pass (the records it had not
  read were trimmed away after 24 hours, a shard vanished, the stream was
  turned off or replaced), it reads the whole table again and rejoins the
  stream.
- With no usable stream it scans at every refresh and says so in its log, and
  looks for a stream again every hour. If you turn the stream off and on again
  it follows the new one.

The stream is read at `streams.dynamodb.REGION.amazonaws.com`. If the gallery
runs where outgoing connections are limited, allow that host as well as
`dynamodb.REGION.amazonaws.com`.

## AWS console

Sign in as someone who may manage IAM.

1. Open **IAM → Policies → Create policy**.
2. Choose the **JSON** tab, delete what is there and paste the edited policy.
3. Choose **Next**, name it `dalibot-gallery-read` and create it.
4. Open **IAM → Users → Create user**. Name it `dalibot-gallery` and leave
   console access off.
5. On the permissions step choose **Attach policies directly**, search for
   and tick `dalibot-gallery-read`, and finish creating the user.
6. Open the new user and choose **Create access key** in the **Summary**
   panel at the top (the same button is on the **Security credentials**
   tab). Choose "Application running outside AWS".
7. AWS now shows two values. Copy both, or download the CSV:
   - **Access key ID** (starts with `AKIA`): this is `AWS_ACCESS_KEY_ID`.
   - **Secret access key**: this is `AWS_SECRET_ACCESS_KEY`. It is shown
     only once; if you lose it, delete the key and create another.

## AWS CLI

As an administrator, save the edited policy as `gallery-policy.json` and run:

```sh
aws iam create-user --user-name dalibot-gallery
aws iam put-user-policy --user-name dalibot-gallery \
  --policy-name dalibot-gallery-read --policy-document file://gallery-policy.json
aws iam create-access-key --user-name dalibot-gallery
```

The last command prints `AccessKeyId`, which is `AWS_ACCESS_KEY_ID`, and
`SecretAccessKey`, which is `AWS_SECRET_ACCESS_KEY`. This attaches
the policy inline to the user, where the console steps create a separate
managed policy; the permissions are the same.

## Give the key to the gallery

1. On the machine that runs the gallery, open the gallery's `.env`, the same
   file that holds `GALLERY_TOKEN` and `TELEGRAM_BOT_TOKEN` (copy
   `.env.example` if you have not made one yet).
2. Add the two values, with no quotes and no spaces around `=`:

   ```sh
   AWS_ACCESS_KEY_ID=AKIA...
   AWS_SECRET_ACCESS_KEY=...
   ```

3. Set `AWS_REGION` and `DALIBOT_TABLE` only if the bot's table is not in
   `ap-southeast-1` or is not called `dalibot`.
4. Make the file readable only by the account that runs the gallery, and
   never commit it:

   ```sh
   chmod 600 .env
   ```

5. Start or restart the gallery, then check it:

   ```sh
   curl -i http://127.0.0.1:8080/healthz
   ```

   It answers 200 once the first read of the table has succeeded. If it
   stays at 503, the gallery's log names the AWS error; see
   [Troubleshooting](#troubleshooting).

The secret access key belongs only in `.env`. Do not paste it into chats,
tickets or commit messages; if it leaks, delete the key in IAM and create a
new one.

The gallery uses the standard AWS credential chain, so `AWS_PROFILE` works
as well when it does not run in a container.

## Running on AWS

On EC2 or ECS, attach the same policy to the instance or task role instead
of creating a user, and leave `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY` unset.

## Cost

With the stream enabled, the gallery scans the table once at start-up (and
every 6 hours, as a check; those scans are strongly consistent, which costs
twice as much per item) and then reads only the changes: per refresh, one
`DescribeStream` and a `GetRecords` call or two for each shard. DynamoDB gives
a monthly allowance of stream reads, and a gallery refreshing every minute
is far below it (check the current pricing for your region).

Without the stream, every refresh is a scan of the whole table, billed as
read capacity for every item (half a read unit per 4 KB, as the scan is
eventually consistent). A table of 5,000 items of about 1 KB scanned every
minute is roughly 900,000 read units a day. On a table with provisioned
capacity each scan is a burst that can also throttle the bot. Enable the
stream, or raise `GALLERY_REFRESH`.

## Troubleshooting

`/healthz` answers 503 until the first read of the table has succeeded, and
the gallery's log says why. The usual causes:

- `AccessDeniedException`: the policy is not attached to the identity in
  use, or its `Resource` names the wrong region, account or table. The
  error quotes the table the gallery tried to read, for example
  `arn:aws:dynamodb:us-east-2:123456789012:table/dalibot`. Compare it with
  the policy's `Resource` line: they must match exactly. If the region in
  the error is not where the table is, fix `AWS_REGION` in `.env`; if it
  is, fix the policy.
- A warning "reading the whole table at every refresh, which costs more than
  following its stream", with the reason after it: "the table has no stream"
  (enable it, above), "the table cannot be described" or "the table's stream
  cannot be read" (the policy lacks the actions the message names), or "the
  table's stream has a view type the gallery cannot use" (it must be New
  image). The gallery works meanwhile, by scanning.
- "could not look for the table's stream; reading the whole table": the lookup
  failed in a way that may pass (the network, throttling). The gallery tries
  again at the next refresh.
- A warning "following the table's stream failed; reading the whole table
  again", with the error after it: the gallery had to start over, as described
  above. It is only a problem if it repeats at every refresh; then the error
  says what is wrong. A stream that fails twice within 10 minutes is left
  alone for an hour.
- `ResourceNotFoundException`: `AWS_REGION` or `DALIBOT_TABLE` does not
  match where the bot's table is.
- `UnrecognizedClientException` or `InvalidSignatureException`: the access
  key or secret is wrong, or was pasted with quotes or spaces.

To check the key by itself, with the same variables exported:

```sh
aws dynamodb scan --table-name dalibot --region ap-southeast-1 --select COUNT
```
