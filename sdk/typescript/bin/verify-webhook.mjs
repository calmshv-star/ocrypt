#!/usr/bin/env node
/** Run after `pnpm build`. Secrets are read from a mode-restricted file, never argv. */
import { readFile, stat } from "node:fs/promises";
import { verifyWebhook } from "../dist/index.js";
const args=Object.fromEntries(process.argv.slice(2).map((value)=>value.split(/=(.*)/s).slice(0,2)));
for(const required of ["--body","--signature","--content-digest","--key-id","--secret-file"])if(!args[required])throw new Error(`missing ${required}`);
const info=await stat(args["--body"]);if(info.size<1||info.size>1_048_576)throw new Error("fixture body exceeds 1 MiB");const secretInfo=await stat(args["--secret-file"]);if((secretInfo.mode&0o077)!==0)throw new Error("secret file must not be group/world accessible");
const [raw,secret]=await Promise.all([readFile(args["--body"]),readFile(args["--secret-file"],"utf8")]);
const value=await verifyWebhook({rawBody:raw,signatureHeader:args["--signature"],contentDigest:args["--content-digest"],resolveSecret:(key)=>key===args["--key-id"]?secret.trim():undefined,now:args["--now"]?Number(args["--now"]):undefined});
process.stdout.write(JSON.stringify({verified:true,event_id:value.eventId,event_type:value.event.event_type,key_id:value.keyId})+"\n");
