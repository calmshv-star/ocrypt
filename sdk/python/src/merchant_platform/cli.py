"""Offline webhook fixture verifier. It never prints raw bodies or secrets."""
import argparse, json, stat, time
from pathlib import Path
from .webhooks import verify_webhook

def main() -> int:
    parser=argparse.ArgumentParser(prog="merchant-webhook-verify")
    parser.add_argument("--body",type=Path,required=True);parser.add_argument("--signature",required=True);parser.add_argument("--content-digest",required=True);parser.add_argument("--key-id",required=True);parser.add_argument("--secret-file",type=Path,required=True);parser.add_argument("--now",type=int,default=int(time.time()));parser.add_argument("--max-bytes",type=int,default=1_048_576)
    args=parser.parse_args();size=args.body.stat().st_size
    if size<1 or size>args.max_bytes: raise SystemExit("fixture body exceeds configured bound")
    if stat.S_IMODE(args.secret_file.stat().st_mode)&0o077: raise SystemExit("secret file must not be group/world accessible")
    raw=args.body.read_bytes();secret=args.secret_file.read_text(encoding="utf-8").strip()
    verified=verify_webhook(raw,args.signature,args.content_digest,lambda key: secret if key==args.key_id else None,now=args.now)
    print(json.dumps({"verified":True,"event_id":verified.event_id,"event_type":verified.event.event_type,"key_id":verified.key_id},separators=(",",":")))
    return 0
if __name__=="__main__":raise SystemExit(main())
