package com.merchantplatform;

import java.nio.charset.StandardCharsets;
import java.security.KeyFactory;
import java.security.MessageDigest;
import java.security.Signature;
import java.security.spec.X509EncodedKeySpec;
import java.util.Arrays;
import java.util.Base64;
import java.util.Map;

public final class Reports {
    private Reports(){}
    public static void verify(byte[]raw,Models.ReconciliationReport report,Map<String,byte[]>publicKeys)throws Exception{if(!"ready".equals(report.status()))throw new IllegalArgumentException("report is not ready");byte[]digest=MessageDigest.getInstance("SHA-256").digest(raw);if(!hex(digest).equals(report.objectSha256()))throw new IllegalArgumentException("reconciliation report digest mismatch");if(report.objectSizeBytes()!=null&&Long.parseLong(report.objectSizeBytes())!=raw.length)throw new IllegalArgumentException("reconciliation report size mismatch");byte[]rawKey=publicKeys.get(report.signingKeyId());if(rawKey==null)throw new IllegalArgumentException("unknown reconciliation signing key: "+report.signingKeyId());byte[]prefix=hexBytes("302a300506032b6570032100"),encoded=Arrays.copyOf(prefix,prefix.length+rawKey.length);System.arraycopy(rawKey,0,encoded,prefix.length,rawKey.length);Signature verifier=Signature.getInstance("Ed25519");verifier.initVerify(KeyFactory.getInstance("Ed25519").generatePublic(new X509EncodedKeySpec(encoded)));verifier.update(message(report.id(),report.snapshotLedgerSequence(),digest));if(!verifier.verify(Base64.getUrlDecoder().decode(report.signature())))throw new IllegalArgumentException("reconciliation report signature mismatch");}
    public static byte[]message(String id,String sequence,byte[]digest){byte[]prefix=("merchant-reconciliation-jsonl-v1\0"+id+"\0"+sequence+"\0").getBytes(StandardCharsets.UTF_8),result=Arrays.copyOf(prefix,prefix.length+digest.length);System.arraycopy(digest,0,result,prefix.length,digest.length);return result;}
    private static String hex(byte[]value){StringBuilder out=new StringBuilder();for(byte item:value)out.append(String.format("%02x",item));return out.toString();}
    private static byte[]hexBytes(String value){byte[]result=new byte[value.length()/2];for(int i=0;i<result.length;i++)result[i]=(byte)Integer.parseInt(value.substring(i*2,i*2+2),16);return result;}
}
