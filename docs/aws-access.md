# AWS access

The gallery reads the bot's DynamoDB table and never writes to it. Give it
its own read-only identity. Do not reuse the bot's user or Lambda role: the
bot's policy can write, and it has no `Scan`.

Follow the steps in order. Steps 1 to 4 happen in the AWS console, signed in
as someone who may manage IAM; steps 5 and 6 happen on the machine that runs
the gallery. The screen names are those of the AWS console when this was
written and may change. If you prefer the command line, steps 2 to 4 have a
[CLI equivalent](#aws-cli-instead-of-steps-2-to-4).

Contents: [1. Collect three facts](#step-1-collect-three-facts) ·
[2. Create the policy](#step-2-create-the-policy) ·
[3. Create the user](#step-3-create-the-user) ·
[4. Create the access key](#step-4-create-the-access-key) ·
[5. Put the key in .env](#step-5-put-the-key-in-env) ·
[6. Start the gallery and check](#step-6-start-the-gallery-and-check) ·
[AWS CLI](#aws-cli-instead-of-steps-2-to-4) ·
[Running on AWS](#running-on-aws) · [Cost](#cost) · [Troubleshooting](#troubleshooting)

## Step 1: Collect three facts

You need these for the policy in step 2:

1. **Account ID**: your 12-digit AWS account number. It is in the menu at the
   top right of the console.
2. **Region**: where the bot's table is. The gallery assumes
   `ap-southeast-1` unless `AWS_REGION` says otherwise.
3. **Table name**: `dalibot`, unless the bot sets `DALIBOT_TABLE`.

## Step 2: Create the policy

1. Open **IAM → Policies** and choose **Create policy**.
2. On **Specify permissions**, switch the policy editor to **JSON** and
   replace what is there with:

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

3. In the `Resource` line, replace `REGION` and `ACCOUNT_ID` with the values
   from step 1, and `dalibot` if the table has another name.
4. Choose **Next**.
5. On **Review and create**, enter the policy name `dalibot-gallery-read`.
6. Choose **Create policy**.

The current code only calls `Scan`, so `dynamodb:GetItem` can be left out.
Add `kms:Decrypt` on the key only if the table is encrypted with a
customer-managed KMS key.

## Step 3: Create the user

1. Open **IAM → Users** and choose **Create user**.
2. On **Specify user details**, enter the user name `dalibot-gallery`. Leave
   the option for access to the AWS Management Console unticked.
3. Choose **Next**.
4. On **Set permissions**, under **Permissions options**, select **Attach
   policies directly**. (The console preselects **Add user to group**; a
   group is not needed for a single user.)
5. In the **Permissions policies** list that appears, search for
   `dalibot-gallery-read` and tick it. Tick nothing else.
6. Leave **Set permissions boundary** alone and choose **Next**.
7. On **Review and create**, check that the only policy listed is
   `dalibot-gallery-read`, then choose **Create user**.

If you reached **Set permissions** before making the policy, the
**Create policy** button above the policy list opens step 2 in a new tab.
Finish it there, come back, and refresh the list.

## Step 4: Create the access key

1. In **IAM → Users**, open `dalibot-gallery`.
2. Open the **Security credentials** tab.
3. Under **Access keys**, choose **Create access key**.
4. For the use case, select **Application running outside AWS**, then **Next**.
5. The description tag is optional. Choose **Create access key**.
6. On **Retrieve access keys**, copy both values or choose **Download .csv
   file**:
   - **Access key**: starts with `AKIA`. This is `AWS_ACCESS_KEY_ID`.
   - **Secret access key**: this is `AWS_SECRET_ACCESS_KEY`. It is shown
     only on this screen. If you lose it, delete the key and create another.
7. Choose **Done**.

## Step 5: Put the key in .env

1. On the machine that runs the gallery, open the gallery's `.env`, the same
   file that holds `GALLERY_TOKEN` and `TELEGRAM_BOT_TOKEN`. Copy
   `.env.example` if you have not made one yet.
2. Add the two values, with no quotes and no spaces around `=`:

   ```sh
   AWS_ACCESS_KEY_ID=AKIA...
   AWS_SECRET_ACCESS_KEY=...
   ```

3. Set `AWS_REGION` and `DALIBOT_TABLE` only if the bot's table is not in
   `ap-southeast-1` or is not called `dalibot`.
4. Make the file readable only by the account that runs the gallery:

   ```sh
   chmod 600 .env
   ```

The secret access key belongs only in `.env`. Never commit the file, and do
not paste the secret into chats, tickets or commit messages. If it leaks,
delete the key in IAM (step 4, same screen) and create a new one.

## Step 6: Start the gallery and check

1. Start or restart the gallery.
2. Ask for its health:

   ```sh
   curl -i http://127.0.0.1:8080/healthz
   ```

3. It answers 200 once the first read of the table has succeeded. If it
   stays at 503, the gallery's log names the AWS error; see
   [Troubleshooting](#troubleshooting).

## AWS CLI instead of steps 2 to 4

As an administrator, save the policy from step 2, edited as described
there, as `gallery-policy.json` and run:

```sh
aws iam create-user --user-name dalibot-gallery
aws iam put-user-policy --user-name dalibot-gallery \
  --policy-name dalibot-gallery-read --policy-document file://gallery-policy.json
aws iam create-access-key --user-name dalibot-gallery
```

The last command prints `AccessKeyId`, which is `AWS_ACCESS_KEY_ID`, and
`SecretAccessKey`, which is `AWS_SECRET_ACCESS_KEY`. Continue with step 5.

This attaches the policy inline to the user, where the console steps create
a separate managed policy; the permissions are the same. Your account ID,
if you need it for the policy, is printed by
`aws sts get-caller-identity --query Account --output text`.

## Running on AWS

On EC2 or ECS, do step 2, then attach the policy to the instance or task
role instead of creating a user. Skip steps 3 to 5 and leave
`AWS_ACCESS_KEY_ID` and `AWS_SECRET_ACCESS_KEY` unset. Outside a container,
`AWS_PROFILE` also works, because the gallery uses the standard AWS
credential chain.

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
