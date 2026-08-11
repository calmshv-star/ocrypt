import { expect, test, type BrowserContext } from "@playwright/test";

import { expectNamedInteractiveControls, expectNoHorizontalOverflow, preferReducedMotion } from "./helpers";

const adminURL = process.env.ADMIN_MANAGEMENT_E2E_URL;

async function addCSRFCookie(context:BrowserContext, origin:string, value:string) {
  const secure = new URL(origin).protocol === "https:";
  await context.addCookies([{name:secure?"__Host-admin_csrf":"admin_csrf_e2e",value,url:origin,secure,sameSite:"Strict"}]);
}

if (!adminURL) {
  test("admin management fixture is available", () => {
    test.skip(true, "ADMIN_MANAGEMENT_E2E_URL is not set");
  });
} else {
  test("payment link creation preserves one-time capability semantics", async ({ context, page }) => {
    await preferReducedMotion(page);
    const origin = new URL(adminURL).origin;
    const tenantID = "10000000-0000-4000-8000-000000000001";
    const merchantID = "10000000-0000-4000-8000-000000000002";
    const publicURL = "https://checkout.example/pay?token=pl_browser_once";
    let requestBody: Record<string, unknown> | undefined;
    let idempotency = "";

    await addCSRFCookie(context, origin, "c".repeat(43));
    await page.route("**/admin/v1/session/me", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ user_id:"10000000-0000-4000-8000-000000000003",session_id:"10000000-0000-4000-8000-000000000004",display_name:"Browser Operator",roles:["security_admin"],permissions:["payment_links:read","payment_links:write"],scopes:[{tenant_id:tenantID,merchant_id:merchantID}],amr:["mfa"] }) }));
    await page.route("**/admin/v1/payment-links?**", (route) => route.fulfill({ contentType:"application/json",body:JSON.stringify({data:[]}) }));
    await page.route("**/admin/v1/payment-links", async (route) => {
      idempotency = route.request().headers()["idempotency-key"] ?? "";
      requestBody = route.request().postDataJSON() as Record<string, unknown>;
      await route.fulfill({ status:201,contentType:"application/json",body:JSON.stringify({ id:"20000000-0000-4000-8000-000000000001",public_url:publicURL,name:"Plan",amount_minor:"3813",currency:"USD",currency_scale:2,description:"Renewal",allowed_routes:[{provider:"on_chain",chain_id:"tron",asset_id:"usdt-trc20"}],metadata:{},success_url:"https://merchant.example/success",cancel_url:"https://merchant.example/cancel",max_uses:1,use_count:0,settled_count:0,settled_minor:"0",status:"active",created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z",version:1 }) });
    });

    await page.goto(`${origin}/#/payment-links`, { waitUntil:"domcontentloaded" });
    await page.getByLabel("Name").fill("Plan");
    await page.getByLabel("Amount in minor units").fill("3813");
    await page.getByLabel("Currency", { exact:true }).fill("usd");
    await page.getByLabel("Currency scale").fill("2");
    await page.getByLabel("Chain ID").fill("tron");
    await page.getByLabel("Asset ID").fill("usdt-trc20");
    await page.getByLabel("Description").fill("Renewal");
    await page.getByLabel("Merchant success URL").fill("https://merchant.example/success");
    await page.getByLabel("Merchant cancel URL").fill("https://merchant.example/cancel");
    await page.getByRole("button", { name:"Create", exact:true }).click();

    await expect(page.getByTestId("one-time-value")).toContainText(publicURL);
    expect(idempotency.length).toBeGreaterThanOrEqual(8);
    expect(requestBody).toMatchObject({amount_minor:"3813",currency:"USD",allowed_routes:[{provider:"on_chain",chain_id:"tron",asset_id:"usdt-trc20"}],max_uses:1});
    expect(await page.evaluate((value) => !JSON.stringify(localStorage).includes(value) && !JSON.stringify(sessionStorage).includes(value), publicURL)).toBe(true);
    await page.getByRole("button", {name:"I saved it"}).click();
    await expect(page.getByTestId("one-time-value")).toHaveCount(0);
    await expectNamedInteractiveControls(page);
    await page.setViewportSize({width:390,height:844});
    await expectNoHorizontalOverflow(page);
  });

  test("a second operator approves a durable destructive action", async ({ context, page }) => {
    const origin=new URL(adminURL).origin;const tenantID="10000000-0000-4000-8000-000000000001";const merchantID="10000000-0000-4000-8000-000000000002";const actionID="70000000-0000-4000-8000-000000000001";let decision:Record<string,unknown>|undefined;let decisionKey="";
    const action={id:actionID,operation:"webhook.disable",resource_type:"webhook_endpoint",resource_id:"70000000-0000-4000-8000-000000000002",resource_version:4,request_reason:"Endpoint ownership compromised",requested_by:"70000000-0000-4000-8000-000000000003",status:"pending_approval",created_at:"2026-08-11T00:00:00Z",expires_at:"2099-08-11T00:10:00Z",updated_at:"2026-08-11T00:00:00Z",version:1};
    await addCSRFCookie(context, origin, "c".repeat(43));
    await page.route("**/admin/v1/session/me",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({user_id:"70000000-0000-4000-8000-000000000004",session_id:"70000000-0000-4000-8000-000000000005",display_name:"Independent Approver",roles:["senior_approver"],permissions:["webhook_settings:disable"],scopes:[{tenant_id:tenantID,merchant_id:merchantID}],amr:["mfa"]})}));
    await page.route("**/admin/v1/management-actions/webhook-disable?**",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({data:[action]})}));
    await page.route(`**/admin/v1/management-actions/webhook-disable/${actionID}`,route=>route.fulfill({contentType:"application/json",body:JSON.stringify(action)}));
    await page.route(`**/admin/v1/management-actions/webhook-disable/${actionID}/approve`,async route=>{decision=route.request().postDataJSON() as Record<string,unknown>;decisionKey=route.request().headers()["idempotency-key"]??"";await route.fulfill({contentType:"application/json",body:JSON.stringify({...action,status:"completed",approved_by:"70000000-0000-4000-8000-000000000004",approval_reason:"Evidence checked",version:2})})});
    await page.goto(`${origin}/#/management-actions`,{waitUntil:"domcontentloaded"});
    await expect(page.getByText("Pending approval").first()).toBeVisible();await page.getByLabel("Reason").fill("Evidence checked");await page.getByRole("button",{name:"Approve request"}).click();
    expect(decision).toEqual({reason:"Evidence checked"});expect(decisionKey.length).toBeGreaterThanOrEqual(8);
  });

  test("matching policy follows draft, approval and activation without a reject route", async ({ context, page }) => {
    const origin=new URL(adminURL).origin;const tenantID="10000000-0000-4000-8000-000000000001";const merchantID="10000000-0000-4000-8000-000000000002";const policyID="80000000-0000-4000-8000-000000000001";const requester="80000000-0000-4000-8000-000000000002";const approver="80000000-0000-4000-8000-000000000003";let actor=requester;let permissions=["matching_policy:read","matching_policy:write"];let policy:Record<string,unknown>|undefined;const calls:string[]=[];
    const base={id:policyID,proposed_version:1,accumulate_partials:true,underpayment_tolerance_bps:25,overpayment_mode:"manual_review",accept_late_within_grace:true,require_same_sender:true,gasfree_enabled:false,gasfree_fee_collectors:[],created_by:requester,created_at:"2026-08-11T00:00:00Z",updated_at:"2026-08-11T00:00:00Z"};
    await addCSRFCookie(context, origin, "d".repeat(43));
    await page.route("**/admin/v1/session/me",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({user_id:actor,session_id:"80000000-0000-4000-8000-000000000004",display_name:"Policy Operator",roles:["operator"],permissions,scopes:[{tenant_id:tenantID,merchant_id:merchantID}],amr:["mfa"]})}));
    await page.route("**/admin/v1/matching-policies?**",route=>route.fulfill({contentType:"application/json",body:JSON.stringify({data:policy?[policy]:[]})}));
    await page.route("**/admin/v1/matching-policies",async route=>{calls.push("create");policy={...base,status:"draft",version:1};await route.fulfill({status:201,contentType:"application/json",body:JSON.stringify(policy)})});
    await page.route(`**/admin/v1/matching-policies/${policyID}`,route=>route.fulfill({contentType:"application/json",body:JSON.stringify(policy)}));
    await page.route(`**/admin/v1/matching-policies/${policyID}/request-approval`,async route=>{calls.push("request-approval");policy={...policy,status:"pending_approval",requested_by:requester,version:2};await route.fulfill({contentType:"application/json",body:JSON.stringify(policy)})});
    await page.route(`**/admin/v1/matching-policies/${policyID}/approve`,async route=>{calls.push("approve");policy={...policy,status:"approved",approved_by:approver,version:3};await route.fulfill({contentType:"application/json",body:JSON.stringify(policy)})});
    await page.route(`**/admin/v1/matching-policies/${policyID}/activate`,async route=>{calls.push("activate");policy={...policy,status:"activated",activated_by:approver,version:4};await route.fulfill({contentType:"application/json",body:JSON.stringify(policy)})});
    await page.goto(`${origin}/#/matching-policies`,{waitUntil:"domcontentloaded"});await page.getByLabel("Automatically accepted underpayment, %").fill("0.25");await page.getByRole("button",{name:"Create policy draft"}).click();await expect(page.getByText("Proposed policy version 1")).toBeVisible();await page.getByLabel("Reason").fill("Policy evidence reviewed");await page.getByRole("button",{name:"Request policy approval"}).click();
    actor=approver;permissions=["matching_policy:read","matching_policy:approve","matching_policy:activate"];await page.reload({waitUntil:"domcontentloaded"});await page.getByLabel("Reason").fill("Independent policy review");await expect(page.getByRole("button",{name:"Approve policy"})).toBeEnabled();await page.getByRole("button",{name:"Approve policy"}).click();await page.getByLabel("Effective for routes created at or after").fill("2099-08-11T12:00");await page.getByRole("button",{name:"Activate policy"}).click();
    expect(calls).toEqual(["create","request-approval","approve","activate"]);expect(page.url()).not.toContain("reject");
  });
}
