# relay-proxy 설계 문서

## 1. 개요

`relay-proxy`는 portal-tunnel 생태계의 릴레이 서버들 앞에 위치하는 **HTTP 역방향 프록시**로, 다음 역할을 수행한다.

- 특정 릴레이 도메인(`*.portal.thumbgo.kr`, `*.portal.rabbitson87.dev` 등)으로 들어오는 요청을 가로채 AppName을 추출한다.
- 해당 앱이 실제로 올라가 있는 릴레이 서버를 인 메모리 상태로 추적한다.
- 서킷 브레이커를 통해 응답하지 않는 릴레이를 자동으로 제외한다.
- 가용한 릴레이들 사이에서 요청을 분산(로드 밸런싱)한다.

### 레지스트리 소스

```
https://raw.githubusercontent.com/gosuda/portal-tunnel/main/registry.json
```

현재 등록된 릴레이 서버 예시:

| 릴레이 도메인 | 주소 |
|---|---|
| portal.thumbgo.kr | https://portal.thumbgo.kr/ |
| portal.rabbitson87.dev | https://portal.rabbitson87.dev/ |
| s-h.day | https://s-h.day/ |
| portal.dawnfullstack.com | https://portal.dawnfullstack.com/ |
| portal.damn.it.com | https://portal.damn.it.com/ |
| kakashit.org | https://kakashit.org |

---

## 2. 도메인 구조 및 요청 흐름

### 2.1 CNAME 패턴

클라이언트는 아래 형식의 도메인으로 앱에 접근한다.

```
{appName}.{relayDomain}
예) gopher.portal.thumbgo.kr
    gopher.portal.rabbitson87.dev
```

- **appName**: portal-tunnel 위에서 동작하는 애플리케이션 식별자
- **relayDomain**: registry.json 에 등록된 릴레이 서버의 도메인

각 릴레이 도메인은 와일드카드 DNS(`*.portal.thumbgo.kr`)로 `relay-proxy` 인스턴스를 가리키도록 CNAME 설정된다.

### 2.2 요청 처리 흐름

```
Client
  │
  │  GET / HTTP/1.1
  │  Host: gopher.portal.thumbgo.kr
  ▼
┌─────────────────────────────────┐
│          relay-proxy            │
│                                 │
│  1. Host 헤더 파싱              │
│     → appName = "gopher"        │
│     → hint    = "portal.thumbgo.kr" │
│                                 │
│  2. AppRegistry 조회            │
│     "gopher" → [relay-A, relay-C] (알려진 릴레이) │
│     없으면 → Discovery 트리거   │
│                                 │
│  3. CircuitBreaker 필터링       │
│     → 닫힌(정상) 릴레이만 후보  │
│                                 │
│  4. LoadBalancer 선택           │
│     → Weighted Round-Robin      │
│                                 │
│  5. 역방향 프록시 전달          │
└───────────────┬─────────────────┘
                │  HTTPS upstream
          ┌─────▼──────┐
          │  relay-A   │  ← portal.thumbgo.kr
          └────────────┘
```

---

## 3. 시스템 컴포넌트

```
relay-proxy/
├── cmd/
│   └── relay-proxy/
│       └── main.go              # 진입점, 설정 로드, 서버 시작
│
├── internal/
│   ├── config/
│   │   └── config.go            # 설정 구조체 (TOML/YAML)
│   │
│   ├── registry/
│   │   ├── fetcher.go           # registry.json 주기적 폴링
│   │   └── model.go             # Relay 구조체
│   │
│   ├── discovery/
│   │   ├── manager.go           # AppRegistry: appName → []Relay 매핑
│   │   └── prober.go            # 릴레이에 앱 존재 여부 실제 연결 확인
│   │
│   ├── circuitbreaker/
│   │   ├── breaker.go           # 개별 릴레이 서킷 브레이커
│   │   └── registry.go          # 릴레이별 브레이커 관리
│   │
│   ├── balancer/
│   │   └── roundrobin.go        # Weighted Round-Robin 로드 밸런서
│   │
│   └── proxy/
│       ├── handler.go           # HTTP 핸들러, Host 파싱, 파이프라인
│       └── reverseproxy.go      # httputil.ReverseProxy 래퍼
│
└── go.mod
```

---

## 4. 컴포넌트 상세 설계

### 4.1 Registry Fetcher

**역할**: registry.json을 주기적으로 가져와 릴레이 목록을 최신 상태로 유지한다.

```
주기: 5분 (설정 가능)
실패 시: 이전 목록 유지 (graceful degradation)
```

**데이터 구조**:
```go
type Relay struct {
    ID      string    // "portal.thumbgo.kr"
    BaseURL *url.URL  // https://portal.thumbgo.kr
}
```

**동작**:
1. HTTP GET `registry.json`
2. 파싱 후 기존 릴레이 목록과 diff
3. 신규 릴레이 → CircuitBreaker 등록, Discovery 워커 시작
4. 제거된 릴레이 → 풀에서 제거 (진행 중인 요청은 완료 후 종료)

---

### 4.2 Discovery Manager (AppRegistry)

**역할**: 각 AppName이 어떤 릴레이에 올라가 있는지 인 메모리로 추적한다.

**핵심 자료구조**:
```
AppRegistry: sync.Map
  key:   appName (string)
  value: *AppEntry

AppEntry:
  relays:    []string          // 이 앱을 서빙 중인 릴레이 ID 목록
  probedAt:  time.Time         // 마지막 탐색 시각
  mu:        sync.RWMutex
```

**앱 탐색 알고리즘**:

```
요청이 들어온 appName이 AppRegistry에 없거나 TTL이 만료된 경우:

1. 모든 정상(Circuit Closed) 릴레이에 대해 병렬 probe 실행
2. probe: {relayBaseURL}/{appName}/ 에 HEAD 요청
   - 200/301/302 → 해당 릴레이에 앱 존재
   - 404 → 앱 없음
   - 연결 실패 → CircuitBreaker에 실패 기록
3. 응답한 릴레이 목록을 AppEntry에 저장
4. TTL: 30초 (설정 가능)
5. 탐색 중 요청은 대기(최대 3초 타임아웃)
```

**캐시 무효화**:
- TTL 만료 → lazy re-probe (요청 시점에 트리거)
- CircuitBreaker가 특정 릴레이를 Open 상태로 전환 → 해당 릴레이를 포함한 모든 AppEntry에서 제거

---

### 4.3 Circuit Breaker

각 릴레이마다 독립된 서킷 브레이커 인스턴스를 유지한다.

**상태 전이**:

```
        실패 임계치 초과
CLOSED ──────────────────► OPEN
  ▲                          │
  │    성공                   │ 대기 시간 경과
  │                          ▼
  └─────────────────── HALF-OPEN
         탐침 요청 성공
```

**파라미터** (설정 가능):

| 파라미터 | 기본값 | 설명 |
|---|---|---|
| `failureThreshold` | 5 | CLOSED → OPEN 전환 실패 횟수 |
| `successThreshold` | 2 | HALF-OPEN → CLOSED 전환 성공 횟수 |
| `timeout` | 30s | OPEN → HALF-OPEN 대기 시간 |
| `halfOpenMaxReqs` | 1 | HALF-OPEN 상태에서 허용할 탐침 요청 수 |

**릴레이 health check**:
- 별도 백그라운드 고루틴이 각 릴레이의 헬스 엔드포인트(`/health` 또는 `/`)를 주기적으로 호출
- 응답 시간 > 임계치(기본 5s) → 실패로 간주
- CircuitBreaker가 Open으로 전환되면 AppRegistry 전체에서 해당 릴레이 제거

---

### 4.4 Load Balancer

AppEntry에 등록된 릴레이들 사이에서 **Weighted Round-Robin** 방식으로 요청을 분산한다.

- 초기 가중치: 균등 (1:1:1...)
- 응답 지연이 낮은 릴레이에 높은 가중치 부여 (EWMA 기반 조정)
- Circuit이 Open인 릴레이는 가중치 0으로 즉시 제외

```
후보 릴레이가 0개인 경우:
  → 503 Service Unavailable 반환
  → 백그라운드에서 re-probe 트리거
```

---

### 4.5 HTTP 핸들러 파이프라인

```go
// 요청 파이프라인 (middleware 체인)
handler = RequestIDMiddleware(
    LoggingMiddleware(
        HostParseMiddleware(       // appName, relayHint 추출
            DiscoveryMiddleware(   // AppRegistry 조회/갱신
                CircuitMiddleware( // 서킷 브레이커 필터
                    BalanceMiddleware(    // 릴레이 선택
                        ReverseProxyHandler, // 전달
                    ),
                ),
            ),
        ),
    ),
)
```

**Host 파싱 규칙**:

```
Host: gopher.portal.thumbgo.kr

1. 알려진 릴레이 도메인 목록과 접미사 매칭
   "portal.thumbgo.kr" ∈ knownRelays → 매칭
2. 접두사 추출: "gopher" → appName
3. 릴레이 힌트: "portal.thumbgo.kr" → 우선 탐색 릴레이
```

릴레이 힌트가 있더라도, 해당 릴레이가 Circuit Open이거나 앱이 없으면 다른 릴레이로 폴백한다.

---

### 4.6 DNS 서버 (선택적 컴포넌트)

프록시를 로컬/온프레미스 환경에서 사용할 때, 별도 DNS 서버 없이 동작하려면 내장 DNS 응답 기능이 필요하다.

```
miekg/dns 패키지 사용

동작:
  - 등록된 릴레이 도메인의 와일드카드 쿼리(*.portal.thumbgo.kr 등)에 대해
    relay-proxy 자신의 IP를 A 레코드로 응답
  - 그 외 쿼리는 업스트림 DNS(8.8.8.8 등)로 포워딩
```

프로덕션 환경에서는 이 컴포넌트 없이, 인프라 수준의 DNS CNAME 설정으로 대체 가능하다.

---

## 5. 데이터 흐름 상세 (시퀀스)

```
Client → Handler → AppRegistry → CircuitBreakerRegistry → Balancer → ReverseProxy → Relay

1. [Handler]        Host 헤더 파싱 → appName="gopher"
2. [AppRegistry]    "gopher" 조회
     2a. HIT (TTL 유효): 릴레이 목록 반환
     2b. MISS / TTL 만료:
          [Prober] 전체 정상 릴레이에 병렬 HEAD probe
          결과 저장 후 릴레이 목록 반환
3. [CircuitBreakerRegistry] 각 릴레이 상태 확인 → 정상 릴레이만 필터
4. [Balancer]       Weighted Round-Robin으로 릴레이 1개 선택
5. [ReverseProxy]   선택된 릴레이로 요청 포워딩
     - upstream: https://{appName}.{relayDomain}/...
     - 응답 실패 시 CircuitBreaker에 실패 기록
     - 재시도: 최대 2회, 다른 릴레이로 폴백
6. [Handler]        응답 반환
```

---

## 6. 설정 파일 구조

```toml
[server]
addr = ":8080"
tls_cert = ""       # TLS 종료를 여기서 할 경우
tls_key  = ""

[dns]
enabled  = false
addr     = ":5353"
upstream = "8.8.8.8:53"

[registry]
url             = "https://raw.githubusercontent.com/gosuda/portal-tunnel/main/registry.json"
refresh_interval = "5m"
http_timeout     = "10s"

[discovery]
probe_ttl        = "30s"   # AppEntry 캐시 유효 시간
probe_timeout    = "3s"    # 앱 탐색 요청 타임아웃
probe_concurrent = 10      # 병렬 probe 최대 수

[circuit_breaker]
failure_threshold  = 5
success_threshold  = 2
open_timeout       = "30s"
health_check_interval = "10s"
health_check_timeout  = "5s"

[balancer]
algorithm = "weighted-round-robin"  # round-robin | random | weighted-round-robin
```

---

## 7. 에러 처리 전략

| 상황 | 처리 |
|---|---|
| 모든 릴레이 Circuit Open | 503 반환, re-probe 예약 |
| appName 탐색 타임아웃 | 503 반환 |
| 업스트림 응답 5xx | CircuitBreaker 실패 카운트 증가, 다른 릴레이로 재시도 |
| 업스트림 연결 실패 | CircuitBreaker 실패 카운트 증가, 즉시 다른 릴레이 폴백 |
| registry.json 갱신 실패 | 기존 릴레이 목록 유지, 에러 로그 기록 |
| 알 수 없는 Host 헤더 | 404 반환 |

---

## 8. 모니터링 엔드포인트 (내부)

```
GET /_relay/health          → relay-proxy 자체 상태
GET /_relay/relays          → 전체 릴레이 목록 + 서킷 상태
GET /_relay/apps            → 인 메모리 AppRegistry 현황
GET /_relay/metrics         → Prometheus 형식 메트릭
```

---

## 9. 주요 의존성

| 용도 | 패키지 |
|---|---|
| HTTP 역방향 프록시 | `net/http/httputil` (표준 라이브러리) |
| DNS 서버 (선택) | `github.com/miekg/dns` |
| 서킷 브레이커 | 직접 구현 또는 `github.com/sony/gobreaker` |
| TOML 설정 | `github.com/BurntSushi/toml` |
| 로깅 | `log/slog` (표준 라이브러리) |

---

## 10. 향후 확장 포인트

- **Sticky Session**: 동일 클라이언트를 동일 릴레이로 라우팅 (쿠키 또는 IP 해시 기반)
- **Rate Limiting**: appName 또는 클라이언트 IP 기준 요청 제한
- **TLS Passthrough**: SNI 기반으로 TLS를 종료하지 않고 릴레이로 투명하게 전달
- **gRPC 지원**: HTTP/2 + trailers 처리
- **Admin API**: 런타임에 릴레이 강제 Open/Close, AppRegistry 수동 갱신
