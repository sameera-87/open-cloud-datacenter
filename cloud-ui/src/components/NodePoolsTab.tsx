import {
  Badge,
  Body1,
  Body2,
  Button,
  Card,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  Dropdown,
  Field,
  MenuItem,
  Option,
  Slider,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Toast,
  ToastTitle,
  Toaster,
  Tooltip,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowClockwise20Regular,
  CloudCube24Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useCallback, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { fmtDate } from '../lib/date';
import { EmptyState, ErrorState, LoadingState } from './list/PageStates';
import { RowActionsMenu } from './list/RowActionsMenu';
import { useListPageStyles } from './list/useListPageStyles';
import { useConfirmDialog } from './useConfirmDialog';
import {
  LabelEditor,
  type LabelPair,
} from './workerPool/LabelEditor';
import {
  TaintEditor,
  type NodePoolTaint,
} from './workerPool/TaintEditor';
import { WorkerPoolForm } from './workerPool/WorkerPoolForm';
import {
  defaultWorkerPoolFormValue,
  workerPoolFormIsValid,
  type WorkerPoolFormValue,
} from './workerPool/workerPoolTypes';

// ── Types ─────────────────────────────────────────────────────────────────────

interface NodePool {
  name: string;
  role: 'system' | 'worker';
  size: 'small' | 'medium' | 'large' | 'xlarge';
  count: number;
  disk_gb?: number;
  taints?: NodePoolTaint[];
  labels?: Record<string, string>;
  status: 'provisioning' | 'ready' | 'scaling' | 'deleting' | 'failed';
  created_at: string;
}

const SIZES = [
  { id: 'small', label: 'Small', specs: '2 vCPU · 4 GB RAM · 40 GB disk' },
  { id: 'medium', label: 'Medium', specs: '4 vCPU · 8 GB RAM · 40 GB disk' },
  { id: 'large', label: 'Large', specs: '8 vCPU · 16 GB RAM · 80 GB disk' },
  { id: 'xlarge', label: 'X-Large', specs: '16 vCPU · 32 GB RAM · 160 GB disk' },
] as const;

type Size = (typeof SIZES)[number]['id'];

// ── Styles ────────────────────────────────────────────────────────────────────

const useStyles = makeStyles({
  tabCard: { padding: tokens.spacingHorizontalL, marginTop: tokens.spacingVerticalM },
  cmdBar: {
    display: 'flex',
    gap: tokens.spacingHorizontalS,
    marginBottom: tokens.spacingVerticalM,
  },
  rolePill: {
    display: 'inline-flex',
    alignItems: 'center',
    padding: `2px ${tokens.spacingHorizontalS}`,
    borderRadius: tokens.borderRadiusCircular,
    fontSize: tokens.fontSizeBase100,
    fontWeight: tokens.fontWeightSemibold,
  },
  systemPill: {
    backgroundColor: tokens.colorPaletteBlueBorderActive,
    color: '#fff',
  },
  workerPill: {
    backgroundColor: tokens.colorPaletteGreenBorderActive,
    color: '#fff',
  },
  dialogForm: {
    display: 'flex',
    flexDirection: 'column',
    gap: tokens.spacingVerticalL,
  },
  infoText: {
    color: tokens.colorNeutralForeground3,
    fontSize: tokens.fontSizeBase200,
  },
  sliderRow: {
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalM,
  },
  sliderValue: {
    minWidth: '80px',
    fontWeight: tokens.fontWeightSemibold,
  },
  warningText: {
    color: tokens.colorPaletteYellowForeground1,
    fontSize: tokens.fontSizeBase200,
    display: 'flex',
    alignItems: 'center',
    gap: tokens.spacingHorizontalXS,
  },
});

// ── Helpers ───────────────────────────────────────────────────────────────────

function statusBadge(status: NodePool['status']) {
  const colorMap: Record<NodePool['status'], 'success' | 'warning' | 'danger' | 'informative'> = {
    ready: 'success',
    provisioning: 'informative',
    scaling: 'informative',
    deleting: 'warning',
    failed: 'danger',
  };
  return (
    <Badge appearance="tint" color={colorMap[status]}>
      {status}
    </Badge>
  );
}

function sizeLabel(s: Size) {
  return SIZES.find((x) => x.id === s)?.label ?? s;
}

// ── Scale dialog ──────────────────────────────────────────────────────────────

interface ScaleDialogProps {
  open: boolean;
  pool: NodePool;
  clusterId: string;
  onClose: () => void;
}

function ScaleDialog({ open, pool, clusterId, onClose }: ScaleDialogProps) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster-scale');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  const SYSTEM_OPTIONS: Array<1 | 3 | 5> = [1, 3, 5];
  const nextSystemOptions = SYSTEM_OPTIONS.filter((n) => n > pool.count);
  const atSystemMax = pool.role === 'system' && pool.count >= 5;

  const [newCount, setNewCount] = useState(() =>
    pool.role === 'system' ? (nextSystemOptions[0] ?? pool.count) : pool.count,
  );

  const scaleMutation = useMutation({
    mutationFn: async () => {
      const { error } = await api.PATCH(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools/{pool_name}',
        {
          params: {
            path: {
              tenant_id: tenantId!,
              project_id: projectId!,
              id: clusterId,
              pool_name: pool.name,
            },
          },
          body: { count: newCount } as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast>
          <ToastTitle>Scale request accepted — pool transitioning</ToastTitle>
        </Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['node-pools', tenantId, projectId, clusterId] });
      onClose();
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast>
          <ToastTitle>Scale failed: {e.message}</ToastTitle>
        </Toast>,
        { intent: 'error' },
      );
    },
  });

  const scalingDown = pool.role === 'worker' && newCount < pool.count;
  const drainCount = pool.count - newCount;

  return (
    <Dialog open={open} onOpenChange={(_, d) => !d.open && onClose()}>
      <Toaster toasterId={toasterId} />
      <DialogSurface>
        <DialogBody>
          <DialogTitle>Scale pool "{pool.name}"</DialogTitle>
          <DialogContent>
            <div className={styles.dialogForm}>
              {pool.role === 'system' ? (
                atSystemMax ? (
                  <Body1>This system pool is already at maximum capacity (5 nodes).</Body1>
                ) : (
                  <>
                    <Body1 className={styles.infoText}>
                      System pool count is constrained to {'{1, 3, 5}'}. Only upward transitions
                      are permitted. Current count: {pool.count}.
                    </Body1>
                    <Field label="New count">
                      <Dropdown
                        value={String(newCount)}
                        selectedOptions={[String(newCount)]}
                        onOptionSelect={(_, d) => setNewCount(Number(d.optionValue) as 1 | 3 | 5)}
                      >
                        {nextSystemOptions.map((n) => (
                          <Option key={n} value={String(n)} text={String(n)}>
                            {n} node{n !== 1 ? 's' : ''}{' '}
                            {n === 3 ? '(HA)' : n === 5 ? '(large HA)' : '(dev)'}
                          </Option>
                        ))}
                      </Dropdown>
                    </Field>
                  </>
                )
              ) : (
                <>
                  <Field label="Node count" hint="1 to 50 nodes.">
                    <div className={styles.sliderRow}>
                      <Slider
                        min={1}
                        max={50}
                        step={1}
                        value={newCount}
                        onChange={(_, d) => setNewCount(d.value)}
                        style={{ flex: 1 }}
                      />
                      <span className={styles.sliderValue}>
                        {newCount} {newCount === 1 ? 'node' : 'nodes'}
                      </span>
                    </div>
                  </Field>
                  {scalingDown && (
                    <div className={styles.warningText}>
                      This will drain {drainCount} node{drainCount > 1 ? 's' : ''}. Running
                      workloads will be rescheduled or evicted.
                    </div>
                  )}
                </>
              )}
            </div>
          </DialogContent>
          <DialogActions>
            <Button appearance="subtle" onClick={onClose} disabled={scaleMutation.isPending}>
              Cancel
            </Button>
            <Button
              appearance="primary"
              onClick={() => scaleMutation.mutate()}
              disabled={
                scaleMutation.isPending ||
                atSystemMax ||
                (pool.role === 'system' && nextSystemOptions.length === 0) ||
                newCount === pool.count
              }
            >
              {scaleMutation.isPending ? 'Scaling…' : 'Scale'}
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

// ── Edit taints/labels dialog ─────────────────────────────────────────────────

interface EditPoolDialogProps {
  open: boolean;
  pool: NodePool;
  clusterId: string;
  onClose: () => void;
}

function EditPoolDialog({ open, pool, clusterId, onClose }: EditPoolDialogProps) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster-edit');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  const [taints, setTaints] = useState<NodePoolTaint[]>(() => pool.taints ?? []);
  const [labels, setLabels] = useState<LabelPair[]>(() =>
    Object.entries(pool.labels ?? {}).map(([key, value]) => ({ key, value })),
  );

  const patchMutation = useMutation({
    mutationFn: async () => {
      const labelMap: Record<string, string> = {};
      for (const { key, value } of labels) {
        if (key) labelMap[key] = value;
      }
      const { error } = await api.PATCH(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools/{pool_name}',
        {
          params: {
            path: {
              tenant_id: tenantId!,
              project_id: projectId!,
              id: clusterId,
              pool_name: pool.name,
            },
          },
          body: { taints, labels: labelMap } as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast>
          <ToastTitle>Taints/labels updated</ToastTitle>
        </Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['node-pools', tenantId, projectId, clusterId] });
      onClose();
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast>
          <ToastTitle>Update failed: {e.message}</ToastTitle>
        </Toast>,
        { intent: 'error' },
      );
    },
  });

  return (
    <Dialog open={open} onOpenChange={(_, d) => !d.open && onClose()}>
      <Toaster toasterId={toasterId} />
      <DialogSurface style={{ maxWidth: '700px' }}>
        <DialogBody>
          <DialogTitle>Edit pool "{pool.name}"</DialogTitle>
          <DialogContent>
            <div className={styles.dialogForm}>
              <TaintEditor taints={taints} onChange={setTaints} />
              <LabelEditor labels={labels} onChange={setLabels} />
            </div>
          </DialogContent>
          <DialogActions>
            <Button appearance="subtle" onClick={onClose} disabled={patchMutation.isPending}>
              Cancel
            </Button>
            <Button
              appearance="primary"
              onClick={() => patchMutation.mutate()}
              disabled={patchMutation.isPending}
            >
              {patchMutation.isPending ? 'Saving…' : 'Save'}
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

// ── Add worker pool dialog ────────────────────────────────────────────────────

interface AddWorkerPoolDialogProps {
  open: boolean;
  clusterId: string;
  existingPoolNames: string[];
  onClose: () => void;
}

function AddWorkerPoolDialog({
  open,
  clusterId,
  existingPoolNames,
  onClose,
}: AddWorkerPoolDialogProps) {
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster-add');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();

  const [formValue, setFormValue] = useState<WorkerPoolFormValue>(defaultWorkerPoolFormValue);
  const [showErrors, setShowErrors] = useState(false);

  const canSubmit = workerPoolFormIsValid(formValue, existingPoolNames);

  const reset = useCallback(() => {
    setFormValue(defaultWorkerPoolFormValue());
    setShowErrors(false);
  }, []);

  const createMutation = useMutation({
    mutationFn: async () => {
      const labelMap: Record<string, string> = {};
      for (const { key, value } of formValue.labels) {
        if (key) labelMap[key] = value;
      }
      const body: Record<string, unknown> = {
        name: formValue.name,
        size: formValue.size,
        count: formValue.count,
        taints: formValue.taints,
        labels: labelMap,
      };
      if (formValue.diskOverride) body.disk_gb = formValue.diskGb;

      const { error } = await api.POST(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools',
        {
          params: {
            path: { tenant_id: tenantId!, project_id: projectId!, id: clusterId },
          },
          body: body as never,
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast>
          <ToastTitle>Worker pool creation accepted — provisioning started</ToastTitle>
        </Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['node-pools', tenantId, projectId, clusterId] });
      reset();
      onClose();
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast>
          <ToastTitle>Create failed: {e.message}</ToastTitle>
        </Toast>,
        { intent: 'error' },
      );
    },
  });

  const handleSubmit = () => {
    setShowErrors(true);
    if (!canSubmit) return;
    createMutation.mutate();
  };

  return (
    <Dialog
      open={open}
      onOpenChange={(_, d) => {
        if (!d.open) {
          reset();
          onClose();
        }
      }}
    >
      <Toaster toasterId={toasterId} />
      <DialogSurface style={{ maxWidth: '700px' }}>
        <DialogBody>
          <DialogTitle>Add worker pool</DialogTitle>
          <DialogContent>
            <WorkerPoolForm
              value={formValue}
              onChange={(patch) => setFormValue((prev) => ({ ...prev, ...patch }))}
              existingNames={existingPoolNames}
              showErrors={showErrors}
            />
          </DialogContent>
          <DialogActions>
            <Button
              appearance="subtle"
              onClick={() => {
                reset();
                onClose();
              }}
              disabled={createMutation.isPending}
            >
              Cancel
            </Button>
            <Button
              appearance="primary"
              onClick={handleSubmit}
              disabled={createMutation.isPending}
            >
              {createMutation.isPending ? 'Creating…' : 'Add pool'}
            </Button>
          </DialogActions>
        </DialogBody>
      </DialogSurface>
    </Dialog>
  );
}

// ── Main NodePoolsTab ──────────────────────────────────────────────────────────

interface NodePoolsTabProps {
  clusterId: string;
  clusterStatus: string;
}

export default function NodePoolsTab({ clusterId, clusterStatus }: NodePoolsTabProps) {
  const styles = useStyles();
  const listStyles = useListPageStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster-pools');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();

  const [addOpen, setAddOpen] = useState(false);
  const [scalePool, setScalePool] = useState<NodePool | null>(null);
  const [editPool, setEditPool] = useState<NodePool | null>(null);

  const poolsQuery = useQuery({
    queryKey: ['node-pools', tenantId, projectId, clusterId],
    enabled: Boolean(tenantId) && Boolean(projectId) && Boolean(clusterId),
    queryFn: async () => {
      const { data, error } = await api.GET(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools',
        {
          params: { path: { tenant_id: tenantId!, project_id: projectId!, id: clusterId } },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as NodePool[];
    },
    refetchInterval: (q) => {
      const pools = q.state.data as NodePool[] | undefined;
      const transitioning = pools?.some(
        (p) => p.status === 'provisioning' || p.status === 'scaling' || p.status === 'deleting',
      );
      return transitioning ? 4_000 : false;
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (poolName: string) => {
      const { error } = await api.DELETE(
        '/v1/tenants/{tenant_id}/projects/{project_id}/clusters/{id}/node-pools/{pool_name}',
        {
          params: {
            path: {
              tenant_id: tenantId!,
              project_id: projectId!,
              id: clusterId,
              pool_name: poolName,
            },
          },
        },
      );
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(
        <Toast>
          <ToastTitle>Delete accepted — pool draining</ToastTitle>
        </Toast>,
        { intent: 'success' },
      );
      queryClient.invalidateQueries({ queryKey: ['node-pools', tenantId, projectId, clusterId] });
    },
    onError: (e: Error) => {
      dispatchToast(
        <Toast>
          <ToastTitle>Delete failed: {e.message}</ToastTitle>
        </Toast>,
        { intent: 'error' },
      );
    },
  });

  const onDelete = async (pool: NodePool) => {
    const ok = await confirmDialog({
      title: `Delete worker pool "${pool.name}"?`,
      body: `This drains all ${pool.count} node${pool.count > 1 ? 's' : ''}. Running workloads will be rescheduled or evicted. This cannot be undone.`,
      confirmLabel: 'Delete pool',
      destructive: true,
      typeToConfirm: pool.name,
    });
    if (!ok) return;
    deleteMutation.mutate(pool.name);
  };

  const pools = poolsQuery.data ?? [];
  const clusterActive = clusterStatus === 'ACTIVE';
  const existingNames = pools.map((p) => p.name);

  return (
    <Card className={styles.tabCard}>
      <Toaster toasterId={toasterId} />

      <div className={styles.cmdBar}>
        <Button
          appearance="primary"
          icon={<Add20Regular />}
          onClick={() => setAddOpen(true)}
          disabled={!clusterActive}
        >
          Add worker pool
        </Button>
        <Button
          appearance="subtle"
          icon={<ArrowClockwise20Regular />}
          onClick={() => poolsQuery.refetch()}
          disabled={poolsQuery.isFetching}
        >
          Refresh
        </Button>
        {!clusterActive && (
          <Body2 style={{ color: tokens.colorNeutralForeground3, alignSelf: 'center' }}>
            Available once the cluster is Active.
          </Body2>
        )}
      </div>

      {poolsQuery.isLoading && <LoadingState label="Loading node pools…" />}

      {poolsQuery.isError && !poolsQuery.isLoading && (
        <ErrorState
          message={`Failed to load node pools: ${(poolsQuery.error as Error).message}`}
        />
      )}

      {!poolsQuery.isLoading && !poolsQuery.isError && pools.length === 0 && (
        <EmptyState
          icon={<CloudCube24Regular />}
          title="No node pools yet"
          description="The system pool will appear here once the cluster is active. Add worker pools to schedule your workloads."
        />
      )}

      {!poolsQuery.isLoading && !poolsQuery.isError && pools.length > 0 && (
        <>
          <Table size="small" aria-label="Node pools">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Role</TableHeaderCell>
                <TableHeaderCell>Size</TableHeaderCell>
                <TableHeaderCell>Count</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>Taints</TableHeaderCell>
                <TableHeaderCell>Labels</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {pools.map((pool) => {
                const taintCount = pool.taints?.length ?? 0;
                const labelCount = Object.keys(pool.labels ?? {}).length;
                return (
                  <TableRow key={pool.name}>
                    <TableCell>
                      <span className={listStyles.nameLink}>{pool.name}</span>
                    </TableCell>
                    <TableCell>
                      <span
                        className={`${styles.rolePill} ${
                          pool.role === 'system' ? styles.systemPill : styles.workerPill
                        }`}
                      >
                        {pool.role}
                      </span>
                    </TableCell>
                    <TableCell>{sizeLabel(pool.size)}</TableCell>
                    <TableCell>{pool.count}</TableCell>
                    <TableCell>{statusBadge(pool.status)}</TableCell>
                    <TableCell className={listStyles.tableMutedCell}>
                      {taintCount > 0 ? (
                        <Tooltip
                          content={
                            <div>
                              {pool.taints!.map((t, i) => (
                                <div key={i}>
                                  {t.key}
                                  {t.value ? `=${t.value}` : ''}: {t.effect}
                                </div>
                              ))}
                            </div>
                          }
                          relationship="description"
                        >
                          <span style={{ cursor: 'default', textDecoration: 'underline dotted' }}>
                            {taintCount} taint{taintCount !== 1 ? 's' : ''}
                          </span>
                        </Tooltip>
                      ) : (
                        '—'
                      )}
                    </TableCell>
                    <TableCell className={listStyles.tableMutedCell}>
                      {labelCount > 0 ? (
                        <Tooltip
                          content={
                            <div>
                              {Object.entries(pool.labels!).map(([k, v]) => (
                                <div key={k}>
                                  {k}: {v}
                                </div>
                              ))}
                            </div>
                          }
                          relationship="description"
                        >
                          <span style={{ cursor: 'default', textDecoration: 'underline dotted' }}>
                            {labelCount} label{labelCount !== 1 ? 's' : ''}
                          </span>
                        </Tooltip>
                      ) : (
                        '—'
                      )}
                    </TableCell>
                    <TableCell className={listStyles.tableMutedCell}>
                      {fmtDate(pool.created_at)}
                    </TableCell>
                    <TableCell>
                      <RowActionsMenu>
                        {pool.role === 'system' && (
                          <MenuItem
                            onClick={() => setScalePool(pool)}
                            disabled={!clusterActive || pool.count >= 5 || pool.status !== 'ready'}
                          >
                            {pool.count >= 5 ? 'Scale up (at maximum)' : 'Scale up'}
                          </MenuItem>
                        )}
                        {pool.role === 'worker' && (
                          <>
                            <MenuItem
                              onClick={() => setScalePool(pool)}
                              disabled={!clusterActive || pool.status !== 'ready'}
                            >
                              Scale
                            </MenuItem>
                            <MenuItem
                              onClick={() => setEditPool(pool)}
                              disabled={!clusterActive || pool.status !== 'ready'}
                            >
                              Edit taints/labels
                            </MenuItem>
                            <MenuItem
                              onClick={() => onDelete(pool)}
                              disabled={
                                !clusterActive ||
                                deleteMutation.isPending ||
                                pool.status === 'deleting'
                              }
                            >
                              Delete
                            </MenuItem>
                          </>
                        )}
                      </RowActionsMenu>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          <Body1
            style={{ color: tokens.colorNeutralForeground3, marginTop: tokens.spacingVerticalM }}
          >
            {pools.length} pool{pools.length !== 1 ? 's' : ''} ·{' '}
            {pools.reduce((acc, p) => acc + p.count, 0)} total nodes
          </Body1>
        </>
      )}

      <AddWorkerPoolDialog
        open={addOpen}
        clusterId={clusterId}
        existingPoolNames={existingNames}
        onClose={() => setAddOpen(false)}
      />

      {scalePool && (
        <ScaleDialog
          open={true}
          pool={scalePool}
          clusterId={clusterId}
          onClose={() => setScalePool(null)}
        />
      )}

      {editPool && (
        <EditPoolDialog
          open={true}
          pool={editPool}
          clusterId={clusterId}
          onClose={() => setEditPool(null)}
        />
      )}
    </Card>
  );
}
