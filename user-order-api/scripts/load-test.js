import http from 'k6/http';
import { check } from 'k6';

const baseURL = __ENV.BASE_URL || 'http://api:8888';
const rate = Number(__ENV.RATE || 50);
const duration = __ENV.DURATION || '30s';

export const options = {
  scenarios: {
    health: {
      executor: 'constant-arrival-rate',
      rate: rate,
      timeUnit: '1s',
      duration: duration,
      preAllocatedVUs: Number(__ENV.PRE_ALLOCATED_VUS || 20),
      maxVUs: Number(__ENV.MAX_VUS || 100),
    },
  },
  thresholds: {
    http_req_failed: ['rate<0.01'],
    http_req_duration: ['p(95)<500'],
  },
};

export default function () {
  const response = http.get(`${baseURL}/api/v1/health`, {
    tags: { endpoint: 'api_health' },
  });
  check(response, { 'health status is 200': (res) => res.status === 200 });
}
