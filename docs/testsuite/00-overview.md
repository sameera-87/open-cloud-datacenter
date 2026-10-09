# 00 — Test Suite at a Glance

This page shows the whole test suite in diagrams. Each section links to the document with the
details. The diagrams use Mermaid, which GitHub renders. In VS Code, use a Markdown preview
extension with Mermaid support.

## 1. The big picture

```mermaid
flowchart LR
    eng([Engineer])

    subgraph build["1 · Build"]
        repo["Provider repo<br/>internal/acctest/*_test.go"]
        img[("Registry<br/>dcapi-acc:&lt;git sha&gt;<br/>test binary + Terraform CLI")]
        repo -->|"make acc-image"| img
    end

    subgraph argo["2 · Run — Argo namespace dcapi-tf-acc"]
        suite["dcapi-acc-suite<br/>(master)"]
        subgraph res["Per-resource WorkflowTemplates — all start at once"]
            direction TB
            net["Networking<br/>vnet · subnet · nsg · nsg-attachment<br/>route-table · rt-association · vnet-peering"]
            dns["DNS<br/>private-dns-zone · dns-record"]
            sec["Secrets<br/>key-vault · kv-secret · private-endpoint"]
            cmp["Compute<br/>virtual-machine · bastion · cluster (+ node pool)"]
            idn["Identity + read-only<br/>service-account · data-sources"]
        end
        sweep["sweep<br/>(onExit)"]
        suite --> net & dns & sec & cmp & idn
        res -.->|"run finished,<br/>pass or fail"| sweep
    end

    subgraph dc["3 · Target"]
        api["DC-API<br/>project tf-acc"]
        plat["KubeOVN · Harvester · Rancher"]
        api --> plat
    end

    eng -->|"build + push"| repo
    eng -->|"argo submit -p image=…"| suite
    img -.->|"pulled by every pod"| res
    res -->|"create · update · import · destroy"| api
    sweep -->|"delete acc-&lt;run&gt;-*"| api
    eng -->|"read results in Argo UI / logs"| suite
```

- The **Go tests** decide what is tested and whether it passed ([03](03-acceptance-test-framework.md)).
- **Argo** only starts one pod per resource, in parallel, and cleans up at the end ([05](05-argo-workflows.md)).
- There are no schedules and no test tiers. Someone starts a run, and it runs everything.

## 2. The two kinds of Argo template

```mermaid
flowchart TB
    subgraph master["dcapi-acc-suite (master)"]
        direction TB
        params["arguments<br/>image · region · vm-image · vm-image-display-name<br/>cluster-image · k8s-version · legacy-network"]
        mutex["mutex: dcapi-acc-project<br/>(one run at a time)"]
        dag["DAG all-resources<br/>17 tasks · no depends · failFast: false"]
        exit["onExit: sweep<br/>owner credentials"]
    end

    subgraph one["dcapi-acc-&lt;resource&gt; (×18)"]
        direction TB
        test["template: test<br/>pod runs acctest.test -test.run=^TestAcc&lt;Res&gt;_"]
        cleanup["template: cleanup<br/>→ calls master's sweep"]
        own["own arguments + mutex<br/>(used only when submitted alone)"]
    end

    dag -->|"templateRef: test"| test
    cleanup -->|"templateRef: sweep"| exit

    alone(["argo submit --from<br/>workflowtemplate/dcapi-acc-nsg"]) --> own
    full(["argo submit --from<br/>workflowtemplate/dcapi-acc-suite"]) --> params
```

When the master calls a resource template, only its `test` template is used. The resource
template's own arguments, mutex and cleanup apply only when it's submitted on its own.
`dcapi-acc-node-pool` runs the same test as `dcapi-acc-cluster`, so the master leaves it out
([05 §3–4](05-argo-workflows.md#3-a-per-resource-template)).

## 3. Inside one test pod

```mermaid
sequenceDiagram
    autonumber
    participant Argo
    participant Bin as acctest.test<br/>(Go test binary)
    participant TF as Terraform CLI<br/>(pinned in image)
    participant Prov as dcapi provider<br/>(served in-process)
    participant Cli as Go API client<br/>(independent checks)
    participant API as DC-API

    Argo->>Bin: start pod · env from Secret (DCAPI_TOKEN …)<br/>-test.run=^TestAccNSG_
    loop each TestAcc* test, one after another
        Bin->>TF: apply step config
        TF->>Prov: plan / apply over gRPC
        Prov->>API: POST / GET / PUT
        API-->>Prov: 201 · PENDING → ACTIVE
        Bin->>TF: re-plan (must be empty)
        Bin->>Cli: check the object directly
        Cli->>API: GET
        Bin->>TF: destroy (always, even after a failure)
        Bin->>Cli: CheckDestroy
        Cli->>API: GET → 404
    end
    Bin-->>Argo: exit 0 (pass) or 1 (fail) · --- PASS / --- FAIL in log
```

## 4. What one resource's test checks

```mermaid
flowchart LR
    c["Create<br/>assert attributes"] --> p{"Plan empty<br/>after apply?"}
    p -->|no| fail1["FAIL: perpetual diff"]
    p -->|yes| u["Update in place<br/>plancheck = Update<br/>ID unchanged"]
    u --> r["ForceNew change<br/>plancheck = Replace<br/>new ID, old one gone"]
    r --> i["Import<br/>ImportStateVerify"]
    i --> d["Disappears<br/>delete out-of-band<br/>→ plan wants to recreate"]
    d --> x["Destroy<br/>+ CheckDestroy (404)"]
    v["Validation<br/>plan-only, bad input<br/>→ ExpectError"]
```

Steps that don't apply to a resource are left out. The coverage matrix says which ones each
resource gets ([01 §6](01-scope-and-strategy.md#6-coverage-matrix-what-every-resource-gets)).
Validation tests create nothing and run in seconds.

## 5. What each resource's tests create

Every test builds its own parents and destroys them. Nothing is shared between tests or
resources ([04](04-test-isolation.md)).

```mermaid
flowchart LR
    subgraph own["Parents each test creates for itself"]
        V["VNet"]
        S["Subnet"]
        K["Key vault"]
        N["NSG"]
        RT["Route table"]
        Z["DNS zone"]
        CL["Cluster"]
    end

    subgraph tested["Resource under test"]
        t_vnet["vnet"]
        t_nsg["network_security_group"]
        t_kv["key_vault"]
        t_sa["service_account"]
        t_sub["subnet"]
        t_rt["route_table"]
        t_peer["vnet_peering"]
        t_dz["private_dns_zone"]
        t_nsga["nsg_attachment"]
        t_rta["route_table_association"]
        t_dr["dns_record"]
        t_vm["virtual_machine"]
        t_bas["bastion"]
        t_kvs["key_vault_secret"]
        t_pe["private_endpoint"]
        t_np["node_pool"]
    end

    V --> t_sub & t_rt & t_peer & t_dz
    V --> S
    S --> t_nsga & t_rta & t_vm & t_bas & t_pe
    S --> CL
    N --> t_nsga
    RT --> t_rta
    Z --> t_dr
    V --> Z
    K --> t_kvs & t_pe
    CL --> t_np
```

`vnet`, `network_security_group`, `key_vault` and `service_account` need no parent. The cluster
and node pool are tested together as one chained test
([resources/cluster.md](resources/cluster.md#why-cluster-and-node-pool-are-one-chained-test)).

### Address ranges

Each resource has its own range, so parallel resources never overlap. Tests inside a resource run
one after another and reuse it ([04 §2](04-test-isolation.md#2-cidr-plan)).

```mermaid
flowchart LR
    subgraph r1["10.200 – 10.209"]
        a0["10.200 subnet"]
        a1["10.201 nsg_attachment"]
        a2["10.202 route_table"]
        a3["10.203 rt_association"]
        a4["10.204 vnet_peering (A)"]
        a5["10.205 private_dns_zone"]
        a6["10.206 dns_record"]
        a7["10.207 private_endpoint"]
        a8["10.208 virtual_machine"]
        a9["10.209 bastion"]
    end
    subgraph r2["10.210 – 10.220"]
        b0["10.210–10.213 vnet"]
        b5["10.215 cluster + node_pool"]
        b6["10.216 service_account (fallback)"]
        b20["10.220 vnet_peering (B)"]
    end
```

## 6. A full run over time

All resources start together. The run lasts as long as the slowest one. These are estimates
until the first real run ([05 §3.3](05-argo-workflows.md#33-values-per-resource)).

```mermaid
gantt
    title One dcapi-acc-suite run (estimated upper bounds)
    dateFormat HH:mm
    axisFormat %H:%M
    section Fast
    data-sources            :00:00, 2m
    nsg                     :00:00, 6m
    service-account         :00:00, 8m
    section Networking
    vnet                    :00:00, 25m
    subnet                  :00:00, 60m
    nsg-attachment          :00:00, 60m
    route-table             :00:00, 30m
    rt-association          :00:00, 60m
    vnet-peering            :00:00, 40m
    section DNS and secrets
    private-dns-zone        :00:00, 20m
    dns-record              :00:00, 35m
    key-vault               :00:00, 20m
    kv-secret               :00:00, 25m
    private-endpoint        :00:00, 45m
    section Compute
    virtual-machine         :00:00, 70m
    bastion                 :00:00, 75m
    cluster + node pool     :crit, cl, 00:00, 90m
    section Exit
    sweep                   :after cl, 5m
```

Many of the long bars come from deleting the last subnet in a VNet, which can take up to 15
minutes ([04 §3](04-test-isolation.md#3-the-last-subnet-delete)).

## 7. Credentials

```mermaid
flowchart LR
    human(["Project owner<br/>(one-time setup)"]) -->|"creates"| sa1["SA tf-acc-runner<br/>role: member"]
    human -->|"creates"| sa2["SA tf-acc-owner<br/>role: owner"]

    sa1 -->|"token"| sec1[["Secret dcapi-acc-member"]]
    sa2 -->|"token"| sec2[["Secret dcapi-acc-owner"]]

    sec1 -->|"envFrom"| most["16 resource templates<br/>+ data-sources"]
    sec2 -->|"envFrom"| sat["dcapi-acc-service-account"]
    sec2 -->|"envFrom"| sw["sweep step"]
```

Tokens reach pods only through `envFrom`, never as Argo parameters. The `member` SA running
nearly everything also proves `member` is enough to manage resources
([02](02-authentication-and-test-environment.md)).

## 8. Cleanup

```mermaid
flowchart TB
    t["Test ends<br/>(pass or fail)"] --> l1["1 · Framework destroy<br/>deletes everything the test created"]
    l1 -->|"normally: nothing left"| done(["Project clean"])
    l1 -->|"pod killed · timeout · destroy error"| l2
    runend["Run ends<br/>(pass or fail)"] --> l2["2 · Sweep step (onExit)<br/>deletes acc-&lt;run&gt;-*<br/>children before parents"]
    l2 --> done
    l2 -->|"DC-API down · object stuck"| l3["3 · Sweep by hand<br/>make sweep RUN_ID=…"]
    l3 --> done
```

The sweep only matches `acc-<this run>-` names, so it can't touch the runner SAs or another
run's objects ([06](06-cleanup.md)).

## 9. Reading a failed run

```mermaid
flowchart TB
    red["Red node in Argo UI"] --> log["argo logs → find --- FAIL"]
    log --> q{"Error message"}
    q -->|"plan was not empty"| a1["Perpetual diff → fix Read / schema"]
    q -->|"Expected a non-empty plan"| a2["Read doesn't clear ID on 404"]
    q -->|"HTTP 409 / quota exceeded"| a3["Leftovers from an earlier run → sweep it"]
    q -->|"timeout waiting for ACTIVE"| a4["DC-API or platform slow → check DC-API"]
    q -->|"every resource fails in seconds"| a5["Token, endpoint or network → 02 §6"]
    a1 & a2 --> repro["Reproduce locally<br/>make testacc TESTARGS='-run ^TestAccX_y$'"]
```

Details: [05 §7](05-argo-workflows.md#7-reading-the-results).

## 10. Rollout

```mermaid
flowchart LR
    p0["Phase 0<br/>Environment<br/>project · quota · 2 SAs<br/>namespace · egress"] --> p1["Phase 1 · local<br/>framework helpers<br/>G10 subnet timeout fix<br/>networking tests + sweepers"]
    p1 --> p2["Phase 2 · Argo<br/>image · resource templates<br/>master + sweep"]
    p2 --> p3["Phase 3<br/>DNS · peering · secrets<br/>identity · compute"]
    p3 --> later["Later, if needed<br/>schedule · PR trigger<br/>notifications · examples validate"]
```

Details: [08](08-rollout-plan.md).
