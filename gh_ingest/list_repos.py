#!/usr/bin/env python3

"""Collect data on the most-starred repos using GitHub's GraphQL API."""

import json
import os
import requests
import time
from datetime import datetime, timedelta

token = open('.token').read().strip()

def query(payload, variables=None):
    r = requests.post(
        'https://api.github.com/graphql',
        headers={'Authorization': f'bearer {token}'},
        json={"query": payload, "variables": variables or {}}
    )
    r.raise_for_status()
    return r.json()

EXTRA = "language:Go"

# https://docs.github.com/en/graphql/reference/objects#repository
repo_query = '''
query popular_repos($start: String, $num: Int!){
  rateLimit {
    cost
    remaining
    resetAt
  }
  search(query: "is:public ''' + EXTRA + ''' %s", type: REPOSITORY, first: $num, after: $start) {
    repositoryCount
    pageInfo {
      hasNextPage
      endCursor
    }
    edges {
      node {
        ... on Repository {
          nameWithOwner
          url
          isArchived
          isDisabled
          isLocked
          isFork
          diskUsage
          createdAt
          updatedAt
          forkCount
          primaryLanguage {
            name
          }
          stargazers {
            totalCount
          }
          watchers {
            totalCount
          }
        }
      }
    }
  }
}
'''

count_query = '''
query {
  rateLimit {
    cost
    remaining
    resetAt
  }
  search(query: "is:public ''' + EXTRA + ''' %s", type: REPOSITORY, first: 1) {
    repositoryCount
  }
}
'''

def get_repos(q, cursor, num):
    return query(repo_query % q, {'start': cursor, 'num': num})['data']


def get_count(q):
    return query(count_query % q)['data']['search']['repositoryCount']


def scrape(q, out_file):
    path = f'responses/{out_file}'
    if os.path.exists(path):
        print('Skipping', path, 'already exists')
        return

    print('Creating', path)

    all_repos = []
    cursor = None
    while True:
        r = get_repos(q, cursor, 100)
        search = r['search']
        pi = search['pageInfo']
        cursor = pi['endCursor']
        has_next = pi['hasNextPage']
        total = search['repositoryCount']
        if total > 1000:
            raise ValueError(f'Too many results for {q}: {total}')
        all_repos += [e['node'] for e in search['edges']]
        print(r['rateLimit'])
        print(len(all_repos), ' / ', total, cursor)
        if not has_next or r['rateLimit']['remaining'] < 10:
            break

    with open(path, 'w') as out:
        json.dump(all_repos, out)

    time.sleep(2)


def split_interval(a, b):
    d = int((b - a) / 2)
    return [(a, a + d), (a + d + 1, b)]


def split_by_days(stars, day_start, day_end):
    start_fmt = day_start.strftime('%Y-%m-%d')
    end_fmt = day_end.strftime('%Y-%m-%d')
    out_file = f'repos.star={stars}.{start_fmt}-{end_fmt}.json'

    q = f'stars:{stars} created:{start_fmt}..{end_fmt}'
    c = get_count(q)
    if c <= 1000:
        print(f'query: {q} has {c} repos')
        scrape(q, out_file)
    else:
        days = (day_end - day_start).days
        if days == 0:
            raise ValueError(f'Can\'t split any more: {stars} / {day_start} .. {day_end}')
        for a, b in split_interval(0, days):
            dt_a = day_start + timedelta(days=a)
            dt_b = day_start + timedelta(days=b)
            split_by_days(stars, dt_a, dt_b)


def scrape_range_days(star_low, star_high=None):
    if star_high is not None:
        stars = f'{star_low}..{star_high}'
    else:
        stars = f'>={star_low}'
    split_by_days(stars, datetime(2007, 1, 1), datetime.utcnow())


if __name__ == '__main__':
    if not os.path.exists('responses'):
        os.mkdir('responses')
    scrape_range_days(5)
