import argparse
from contextlib import closing
import csv
import importlib.util
import json
from pathlib import Path
import re
import sqlite3
import sys
import tempfile
import unittest
from unittest.mock import patch

ROOT=Path(__file__).resolve().parents[2]
sys.path.insert(0,str(ROOT/'scripts'))
spec=importlib.util.spec_from_file_location('recover_intraday',ROOT/'scripts/recover_intraday.py')
r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)

class RecoveryTests(unittest.TestCase):
 def setUp(self):
  temp=tempfile.TemporaryDirectory();self.addCleanup(temp.cleanup);self.root=Path(temp.name)
  self.database=self.root/'source #?.db'
  source=(ROOT/'backend/internal/repository/sqlite/schema.go').read_text()
  schema=re.search(r'const schema = `(.*?)`',source,re.S).group(1)
  with closing(sqlite3.connect(self.database)) as db:
   db.executescript(schema)
   for kind in r.KINDS:
    for i,clock in enumerate(r.CLOCKS):
     stamp='2026-10-22T'+clock+':00+08:00';rid=kind+clock
     db.execute('INSERT INTO collection_run(run_id,snapshot_at,snapshot_kind,rank_type,status,requested_date,actual_trade_date,expected_total,fetched_total,page_count,attempt_count,started_at,duration_ms,error_code,error_message) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)',(rid,stamp,'minute_work',kind,'success','2026-10-22','2026-10-22',2,2,1,1,stamp,1,'',''))
     for rank,code in enumerate(('BK001','BK002'),1):
      db.execute('INSERT INTO rank_intraday_work(run_id,snapshot_at,trade_date,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)',(rid,stamp,'2026-10-22',kind,rank,90,code,'board','15:00:00',10000,.01,1,i,1,i+1,.05,.6,2,0,1,'leader','600001',101,6,1,stamp))
   db.commit()
  self.args=argparse.Namespace(database=self.database,output=self.root/'out',from_date='2026-10-22',to_date='2026-10-22',ohlc=False,raw_directory=None,codes=None,interval=3,max_failures=2,cache_directory=None,overwrite_stale_report=False,browser_fallback=False)
 def change(self,sql):
  with closing(sqlite3.connect(self.database)) as db:db.execute(sql);db.commit()
 def test_valid_recovery_source_unchanged(self):
  before=self.database.read_bytes();report=r.export(self.args)
  self.assertEqual(report['recoverable_components'],2);self.assertEqual(before,self.database.read_bytes())
  self.assertEqual([f['rows'] for f in report['files']],[4,192])
  with (self.args.output/'recovered_board_close.csv').open() as f:row=next(csv.DictReader(f));self.assertNotIn('turnover',row);self.assertEqual(row['funds_postclose_revised'],'False')
 def test_cold_only_recovery_preserves_both_sources(self):
  cold=self.root/'cold.db'
  with closing(sqlite3.connect(self.database)) as source,closing(sqlite3.connect(cold)) as target:
   source.backup(target)
  self.change('DELETE FROM rank_intraday_work')
  self.change('DELETE FROM collection_run')
  self.args.cold_database=cold
  before_hot=self.database.read_bytes();before_cold=cold.read_bytes()
  report=r.export(self.args)
  self.assertEqual(report['recoverable_components'],2)
  self.assertEqual(self.database.read_bytes(),before_hot)
  self.assertEqual(cold.read_bytes(),before_cold)
  self.assertEqual([f['rows'] for f in report['files']],[4,192])
 def test_missing_one_code_rejected(self):
  self.change("DELETE FROM rank_intraday_work WHERE rank_type='industry' AND code='BK002' AND snapshot_at LIKE '%09:35%'")
  self.assertEqual(r.export(self.args)['recoverable_components'],1)
 def test_consistent_truncation_rejected_by_totals(self):
  self.change("DELETE FROM rank_intraday_work WHERE rank_type='industry' AND code='BK002'")
  self.assertEqual(r.export(self.args)['recoverable_components'],1)
 def test_missing_close_not_replaced_by_1455(self):
  self.change("DELETE FROM rank_intraday_work WHERE rank_type='industry' AND snapshot_at LIKE '%15:00%'")
  self.assertEqual(r.export(self.args)['recoverable_components'],1)
 def test_unavailable_money_and_missing_run(self):
  self.change("UPDATE rank_intraday_work SET money_available=0 WHERE rank_type='industry'")
  self.change("DELETE FROM collection_run WHERE rank_type='concept'")
  self.assertEqual(r.export(self.args)['recoverable_components'],0)
 def test_existing_formal_rows_preserved(self):
  with closing(sqlite3.connect(self.database)) as db:
   db.execute("INSERT INTO rank_snapshot(run_id,snapshot_at,trade_date,requested_date,snapshot_kind,rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at) SELECT run_id,snapshot_at,trade_date,trade_date,'daily_close',rank_type,rank,market,code,name,quote_time,latest_price_raw,change_pct,money_available,dark_money,regular_money,main_money_inflow,dark_activity,dark_inflow_ratio,up_count,flat_count,down_count,leader_name,leader_code,source_version,source_sort_flag,source_descending,fetched_at FROM rank_intraday_work WHERE rank_type='industry' AND snapshot_at LIKE '%15:00%'");db.commit()
  self.assertEqual(r.export(self.args)['days'][0]['components'][0]['status'],'preserved_existing')
 def test_utc_grid_and_alias_duplicates(self):
  self.change("UPDATE rank_intraday_work SET snapshot_at=strftime('%Y-%m-%dT%H:%M:%SZ',snapshot_at)")
  self.assertEqual(r.export(self.args)['recoverable_components'],2)
 def test_destination_failure_never_publishes(self):
  with patch.object(r,'append_csv',side_effect=OSError('disk full')):
   with self.assertRaises(OSError):r.export(self.args)
  self.assertFalse(self.args.output.exists())
 def test_existing_output_refused(self):
  self.args.output.mkdir()
  with self.assertRaises(ValueError):r.export(self.args)
 def body(self,**changes):
  data={'code':'BK001','market':90,'klines':['2026-10-22,10,10.5,11,9,1000,10500,20,5,0.5,2']};data.update(changes)
  return json.dumps({'rc':0,'data':data}).encode()
 def test_mismatched_run_timestamp_blocked(self):
  self.change("UPDATE collection_run SET snapshot_at='2026-10-22T14:00:00+08:00' WHERE rank_type='industry'")
  self.assertEqual(r.export(self.args)['recoverable_components'],1)
 def test_api_failure_preserves_valid_funds_and_stops(self):
  self.args.ohlc=True;self.args.max_failures=1
  with patch.object(r,'fetch_daily',side_effect=ValueError('empty reply')) as fetch:
   report=r.export(self.args)
  self.assertEqual(report['recoverable_components'],2)
  self.assertEqual(report['ohlc']['enriched_close_rows'],0)
  self.assertEqual(fetch.call_count,1)
  self.assertEqual([f['rows'] for f in report['files']],[4,192])
 def test_duplicate_daily_dates_rejected(self):
  body=json.loads(self.body());body['data']['klines']*=2
  with self.assertRaises(ValueError):r.parse_daily(json.dumps(body).encode(),90,'BK001','2026-10-22','2026-10-22')
 def test_cache_request_contract_and_checksum(self):
  cache=self.root/'cache';cache.mkdir()
  path=cache/'90.BK001.json';path.write_bytes(self.body())
  receipt={'endpoint':r.ENDPOINT,'contract':{'secid':'90.BK001','klt':'101','fqt':'0','beg':'20261022','end':'20261022'},'sha256':r.sha256(path)}
  path.with_suffix('.receipt.json').write_text(json.dumps(receipt))
  self.assertIsNotNone(r.cached_daily(cache,90,'BK001','2026-10-22','2026-10-22'))
  receipt['contract']['fqt']='1';path.with_suffix('.receipt.json').write_text(json.dumps(receipt))
  with self.assertRaises(ValueError):r.cached_daily(cache,90,'BK001','2026-10-22','2026-10-22')
 def test_cached_ohlc_survives_later_network_failure(self):
  cache=self.root/'cache';cache.mkdir()
  path=cache/'90.BK001.json';path.write_bytes(self.body())
  path.with_suffix('.receipt.json').write_text(json.dumps({'endpoint':r.ENDPOINT,'contract':{'secid':'90.BK001','klt':'101','fqt':'0','beg':'20261022','end':'20261022'},'sha256':r.sha256(path)}))
  self.args.cache_directory=cache;self.args.ohlc=True;self.args.max_failures=1
  with patch.object(r,'fetch_daily',side_effect=ValueError('empty reply')) as fetch:
   report=r.export(self.args)
  self.assertEqual(report['ohlc']['enriched_close_rows'],2)
  self.assertEqual(fetch.call_count,1)
  self.assertTrue(report['ohlc']['outcomes'][0]['from_cache'])
 def test_daily_units_and_jsonp(self):
  bar=r.parse_daily(b'cb('+self.body()+b');',90,'BK001','2026-10-22','2026-10-22')['2026-10-22']
  self.assertEqual(bar['previous_close'],'10.0');self.assertEqual(bar['change_pct'],'0.05');self.assertEqual(bar['turnover_rate'],'0.02')
 def test_daily_identity_dates_and_invalid_ohlc(self):
  cases=[self.body(code='BK002'),self.body(klines=['2026-10-21,10,10.5,11,9,1000,10500,20,5,0.5,2']),self.body(klines=['2026-10-22,10,10.5,10,9,1000,10500,20,5,0.5,2']),self.body(klines=['2026-10-22,NaN,10.5,11,9,1000,10500,20,5,0.5,2'])]
  for body in cases:
   with self.subTest(body=body),self.assertRaises(ValueError):r.parse_daily(body,90,'BK001','2026-10-22','2026-10-22')
 def test_offline_merge_does_not_change_funds(self):
  raw=self.root/'raw';raw.mkdir()
  for code in ('BK001','BK002'):(raw/f'90.{code}.json').write_bytes(self.body(code=code))
  self.args.raw_directory=raw;report=r.export(self.args)
  self.assertEqual(report['ohlc']['enriched_close_rows'],4)
  with (self.args.output/'recovered_board_close_with_ohlc.csv').open() as f:row=next(csv.DictReader(f));self.assertEqual(row['intraday_change_pct'],'0.01');self.assertEqual(row['change_pct'],'0.05');self.assertEqual(row['funds_postclose_revised'],'False')

if __name__=='__main__':unittest.main()
