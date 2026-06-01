# AWS SES Setup for TinyCld

Complete procedure for outbound + inbound email via AWS SES. Substitute your domain and region throughout.

---

## 1. SES Identity (domain verification)

**Console → SES → Verified identities → Create identity**

- Identity type: **Domain**
- Domain: `tiny.adaptive-publishing.com`
- Enable **Easy DKIM** (RSA-2048)
- Enable **DMARC** compliance

SES gives you three CNAME records and a DMARC TXT record. Add them to DNS:

| Type  | Host | Value |
|-------|------|-------|
| CNAME | `<token1>._domainkey.tiny.adaptive-publishing.com` | `<token1>.dkim.amazonses.com` |
| CNAME | `<token2>._domainkey.tiny.adaptive-publishing.com` | `<token2>.dkim.amazonses.com` |
| CNAME | `<token3>._domainkey.tiny.adaptive-publishing.com` | `<token3>.dkim.amazonses.com` |
| TXT   | `_dmarc.tiny.adaptive-publishing.com` | `v=DMARC1; p=quarantine; rua=mailto:dmarc@tiny.adaptive-publishing.com` |

Wait for **DKIM status: Verified** (usually under 5 minutes).

---

## 2. Custom MAIL FROM domain (recommended)

Gives you a verified envelope sender and SPF pass without sharing the root domain SPF.

**Console → SES → Verified identities → tiny.adaptive-publishing.com → Edit → Custom MAIL FROM**

- MAIL FROM domain: `bounce.tiny.adaptive-publishing.com`
- On MX failure: **Use default MAIL FROM**

Add to DNS:

| Type | Host | Value |
|------|------|-------|
| MX  | `bounce.tiny.adaptive-publishing.com` | `10 feedback-smtp.<region>.amazonses.com` |
| TXT | `bounce.tiny.adaptive-publishing.com` | `v=spf1 include:amazonses.com ~all` |

---

## 3. SPF for the root domain

If you send from `@tiny.adaptive-publishing.com` (not just the MAIL FROM subdomain), also publish:

| Type | Host | Value |
|------|------|-------|
| TXT | `tiny.adaptive-publishing.com` | `v=spf1 include:amazonses.com ~all` |

---

## 4. Request production access (leave SES sandbox)

By default SES can only send to verified addresses.

**Console → SES → Account dashboard → Request production access**

Fill out the form (use case, bounce/complaint handling). Approval usually takes < 24 h.

---

## 5. Inbound email — MX record

| Type | Host | Value |
|------|------|-------|
| MX | `tiny.adaptive-publishing.com` | `10 inbound-smtp.<region>.amazonaws.com` |

---

## 6. Inbound email — SNS topic

**Console → SNS → Topics → Create topic**

- Type: **Standard**
- Name: `tinycld-mail-inbound`
- Note the **Topic ARN**

**Add HTTPS subscription:**

- Topics → tinycld-mail-inbound → Create subscription
- Protocol: **HTTPS**
- Endpoint: `https://tiny.adaptive-publishing.com/api/mail/inbound/<webhook-secret>`
  - Get the webhook secret from the TinyCld UI: Mail → Domain → Webhook URLs
- TinyCld auto-confirms the subscription on the first POST

---

## 7. Inbound email — SES receipt rule set

**Console → SES → Email receiving → Rule sets → Create rule set**

- Name: `tinycld-inbound`
- Set as **active rule set**

**Create receipt rule:**

- Rule name: `receive-all`
- Recipients: `tiny.adaptive-publishing.com` (leave blank to match all, or enter the domain)
- Add action: **SNS**
  - SNS topic: `tinycld-mail-inbound`
  - Encoding: **UTF-8**

For large emails (> 150 KB, optional but recommended):

- Also add action: **S3**
  - Bucket: create `tinycld-mail-inbound-<accountid>` (keep it private, no public access)
  - Object key prefix: `inbound/`

---

## 8. S3 bucket policy (if using S3 action)

**Console → S3 → tinycld-mail-inbound-\<accountid\> → Permissions → Bucket policy**

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "AllowSESPuts",
      "Effect": "Allow",
      "Principal": { "Service": "ses.amazonaws.com" },
      "Action": "s3:PutObject",
      "Resource": "arn:aws:s3:::tinycld-mail-inbound-<accountid>/inbound/*",
      "Condition": {
        "StringEquals": { "AWS:SourceAccount": "<your-account-id>" }
      }
    }
  ]
}
```

---

## 9. IAM — create policy

**Console → IAM → Policies → Create policy** (JSON):

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "SESSend",
      "Effect": "Allow",
      "Action": [
        "ses:SendEmail",
        "ses:SendRawMessage"
      ],
      "Resource": "*"
    },
    {
      "Sid": "SESIdentities",
      "Effect": "Allow",
      "Action": [
        "ses:CreateEmailIdentity",
        "ses:DeleteEmailIdentity",
        "ses:GetEmailIdentity",
        "ses:ListEmailIdentities",
        "ses:PutEmailIdentityDkimSigningAttributes",
        "ses:PutEmailIdentityMailFromAttributes"
      ],
      "Resource": "*"
    },
    {
      "Sid": "SESReceiptRules",
      "Effect": "Allow",
      "Action": [
        "ses:DescribeActiveReceiptRuleSet",
        "ses:DescribeReceiptRuleSet",
        "ses:GetReceiptRule"
      ],
      "Resource": "*"
    },
    {
      "Sid": "S3InboundRead",
      "Effect": "Allow",
      "Action": "s3:GetObject",
      "Resource": "arn:aws:s3:::tinycld-mail-inbound-<accountid>/inbound/*"
    }
  ]
}
```

Name it `tinycld-mail-policy`.

---

## 10. IAM — create user and access key

**Console → IAM → Users → Create user**

- Name: `tinycld-mail`
- Attach policy: `tinycld-mail-policy`

**Create access key:**

- User → Security credentials → Create access key
- Use case: **Application running outside AWS**
- Save **Access key ID** and **Secret access key**

---

## 11. TinyCld environment variables

```env
MAIL_PROVIDER=ses
AWS_REGION=us-east-1                          # your SES region
AWS_ACCESS_KEY_ID=AKIA...
AWS_SECRET_ACCESS_KEY=...
SES_S3_BUCKET=tinycld-mail-inbound-<accountid>  # omit if not using S3 action
SES_RECEIPT_RULE_SET=tinycld-inbound
MAIL_FROM_ADDRESS=noreply@tiny.adaptive-publishing.com
```

Or configure per-org via **Settings → Mail** with keys `ses_region`, `ses_access_key_id`, `ses_secret_access_key`, `ses_s3_bucket`, `ses_receipt_rule_set`.

---

## DNS summary

| Type  | Host | Value | Purpose |
|-------|------|-------|---------|
| MX    | `tiny.adaptive-publishing.com` | `10 inbound-smtp.<region>.amazonaws.com` | Receive mail |
| CNAME | `<token1>._domainkey.tiny...` | `<token1>.dkim.amazonses.com` | DKIM signing |
| CNAME | `<token2>._domainkey.tiny...` | `<token2>.dkim.amazonses.com` | DKIM signing |
| CNAME | `<token3>._domainkey.tiny...` | `<token3>.dkim.amazonses.com` | DKIM signing |
| TXT   | `_dmarc.tiny.adaptive-publishing.com` | `v=DMARC1; p=quarantine; ...` | DMARC |
| MX    | `bounce.tiny.adaptive-publishing.com` | `10 feedback-smtp.<region>.amazonses.com` | Custom MAIL FROM |
| TXT   | `bounce.tiny.adaptive-publishing.com` | `v=spf1 include:amazonses.com ~all` | SPF (MAIL FROM) |
| TXT   | `tiny.adaptive-publishing.com` | `v=spf1 include:amazonses.com ~all` | SPF (From header) |
