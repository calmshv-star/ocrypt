#!/usr/bin/env python3
"""Dry-run by default. Keep all container-referenced images and 3 newest per repo.

Only deletes old, unused images whose EVERY tag belongs to an ocrypt-* repo.
Does not remove containers, volumes, databases, or images from other projects.
"""
import argparse
import collections
import json
import subprocess
import time

def docker(*args):
    return subprocess.check_output(['docker', *args], text=True)

def candidates():
    ids = docker('image', 'ls', '-q', '--no-trunc').split()
    images = json.loads(docker('image', 'inspect', *sorted(set(ids)))) if ids else []
    containers = docker('ps', '-aq').split()
    used = {c['Image'] for c in json.loads(docker('inspect', *containers))} if containers else set()
    repos = collections.defaultdict(dict)
    for image in images:
        for tag in image.get('RepoTags') or []:
            repos[tag.rsplit(':', 1)[0]][image['Id']] = image
    retained = set(used)
    for group in repos.values():
        retained.update(i['Id'] for i in sorted(group.values(), key=lambda i: i['Created'], reverse=True)[:3])
    from datetime import datetime, timezone
    cutoff = time.time() - 7 * 86400
    selected = []
    for image in images:
        tags = image.get('RepoTags') or []
        if not tags or image['Id'] in retained:
            continue
        if not all(t.split(':')[0].startswith('ocrypt-') for t in tags):
            continue
        created = datetime.strptime(image['Created'][:19], '%Y-%m-%dT%H:%M:%S').replace(tzinfo=timezone.utc).timestamp()
        if created < cutoff:
            selected.append({'id': image['Id'], 'tags': tags})
    return selected

if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--apply', action='store_true')
    args = parser.parse_args()
    selected = candidates()
    print(json.dumps({'count': len(selected), 'images': selected}), flush=True)
    if args.apply:
        # Recheck references immediately before each deletion. Never force it.
        for image in selected:
            ids = docker('ps', '-aq').split()
            used = {c['Image'] for c in json.loads(docker('inspect', *ids))} if ids else set()
            if image['id'] in used:
                continue
            subprocess.run(['docker', 'image', 'rm', *image['tags']], check=True)
