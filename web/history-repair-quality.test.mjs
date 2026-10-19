import assert from "node:assert/strict";
import {test} from "node:test";
import {formatEnergyToday} from "./energy-today.js";
import {flowReadingsFromStatus} from "./components/energy-flow-readings.js";

test("corrected household energy stays estimated even when another original flow is missing",()=>{
  const today={load_wh:28000,load_quality:"estimated",history_quality:"incomplete",pv_wh:null,import_wh:12000,measured:{load_wh:null}};
  assert.equal(formatEnergyToday(today,"load_wh"),"≈ 28.0 kWh");
  assert.equal(formatEnergyToday(today,"pv_wh"),"Unknown");
  assert.equal(flowReadingsFromStatus({load_w:1200,energy:{today}}).selfPoweredPctToday,null);
  assert.equal(today.measured.load_wh,null);
});

test("endpoint solar and home totals both show estimate markers",()=>{
  const today={load_wh:41000,load_quality:"estimated",pv_wh:37000,pv_quality:"estimated",history_quality:"incomplete",measured:{load_wh:null,pv_wh:null}};
  assert.equal(formatEnergyToday(today,"load_wh"),"≈ 41.0 kWh");
  assert.equal(formatEnergyToday(today,"pv_wh"),"≈ 37.0 kWh");
});
