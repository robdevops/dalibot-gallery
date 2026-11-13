# AWS access

The gallery reads the bot's DynamoDB table and never writes to it. Give it
its own read-only identity. Do not reuse the bot's user or Lambda role: the
bot's policy can write, and it has no `Scan`.

Contents: [The policy](#the-policy) · [AWS console](#aws-console) ·
[AWS CLI](#aws-cli) · [Give the key to the gallery](#give-the-key-to-the-gallery) ·
[Running on AWS](#running-on-aws) · [Cost](#cost) · [Troubleshooting](#troubleshooting)

## The policy

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "ReadOnlyOneTable",
      "Effect": "Allow",
      "Action": ["dynamodb:Scan", "dynamodb:GetItem"],
      "Resource": "arn:aws:dynamodb:REGION:ACCOUNT_ID:table/dalibot"
    }
  ]
}
```

Before you use it, edit the `Resource` line:

- `REGION`: the table's region. The gallery assumes `ap-southeast-1` unless `AWS_REGION` says otherwise.
- `ACCOUNT_ID`: your 12-digit AWS account number, shown in the menu at the
  top right of the console, or printed by
  `aws sts get-caller-identity --query Account --output text`.
- `dalibot`: the table name, if the bot uses another one (`DALIBOT_TABLE`).

The current code only calls `Scan`, so `dynamodb:GetItem` can be left out.
Add `kms:Decrypt` on the key only if the table is encrypted with a
customer-managed KMS key.

## AWS console

Sign in as someone who may manage IAM.

1. Open **IAM → Policies → Create policy**.
2. Choose the **JSON** tab, delete what is there and paste the edited policy.
3. Choose **Next**, name it `dalibot-gallery-read` and create it.
4. Open **IAM → Users → Create user**. Name it `dalibot-gallery` and leave
   console access off.
5. On the permissions step choose **Attach policies directly**, tick
   `dalibot-gallery-read`, and finish creating the user.
6. Open the new user and go to **Security credentials → Create access key**.
   Choose "Application running outside AWS".
7. Copy the access key ID and the secret access key. The secret is shown
   only once.

## AWS CLI

As an administrator, save the edited policy as `gallery-policy.json` and run:

```sh
aws iam create-user --user-name dalibot-gallery
aws iam put-user-policy --user-name dalibot-gallery \
  --policy-name dalibot-gallery-read --policy-document file://gallery-policy.json
aws iam create-access-key --user-name dalibot-gallery
```

The last command prints `AccessKeyId` and `SecretAccessKey`. This attaches
the policy inline to the user, where the console steps create a separate
managed policy; the permissions are the same.

## Give the key to the gallery

Put the two values in the gallery's `.env`, without quotes:

```sh
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
AWS_REGION=ap-southeast-1
```

`AWS_REGION` and `DALIBOT_TABLE` only need setting when the bot does not use
the defaults (`ap-southeast-1` and `dalibot`). The gallery uses the standard AWS
credential chain, so `AWS_PROFILE` works as well when it does not run in a
container.

Keep `.env` readable only by the account that runs the gallery
(`chmod 600 .env`), and never commit it.

## Running on AWS

On EC2 or ECS, attach the same policy to the instance or task role instead
of creating a user, and leave `AWS_ACCESS_KEY_ID` and
`AWS_SECRET_ACCESS_KEY` unset.

## Cost

The gallery scans the whole table every `GALLERY_REFRESH` (60 s by default)
and serves every request from memory. The table is bounded by the bot's
90-day TTL, so this is small, but each scan is billed as read capacity for
the whole table. Raise `GALLERY_REFRESH` if that matters.

## Troubleshooting

`/healthz` answers 503 until the first read of the table has succeeded, and
the gallery's log says why. The usual causes:

- `AccessDeniedException`: the policy is not attached to the identity in
  use, or its `Resource` names the wrong region, account or table.
- `ResourceNotFoundException`: `AWS_REGION` or `DALIBOT_TABLE` does not
  match where the bot's table is.
- `UnrecognizedClientException` or `InvalidSignatureException`: the access
  key or secret is wrong, or was pasted with quotes or spaces.

To check the key by itself, with the same variables exported:

```sh
aws dynamodb scan --table-name dalibot --region ap-southeast-1 --select COUNT
```
