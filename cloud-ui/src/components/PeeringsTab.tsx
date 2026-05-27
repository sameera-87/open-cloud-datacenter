import {
  Body1,
  Button,
  Dialog,
  DialogActions,
  DialogBody,
  DialogContent,
  DialogSurface,
  DialogTitle,
  DialogTrigger,
  Dropdown,
  Field,
  Input,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Option,
  Spinner,
  Switch,
  Table,
  TableBody,
  TableCell,
  TableHeader,
  TableHeaderCell,
  TableRow,
  Toast,
  ToastTitle,
  Toaster,
  makeStyles,
  tokens,
  useId,
  useToastController,
} from '@fluentui/react-components';
import {
  Add20Regular,
  ArrowsBidirectional20Regular,
  MoreHorizontal20Regular,
} from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { Link } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useConfirmDialog } from './useConfirmDialog';
import StatusPill from './StatusPill';

const useStyles = makeStyles({
  root: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalM },
  toolbar: { display: 'flex', gap: tokens.spacingHorizontalS },
  monoCell: { fontFamily: tokens.fontFamilyMonospace, fontSize: tokens.fontSizeBase200 },
  mutedCell: { color: tokens.colorNeutralForeground3, fontSize: tokens.fontSizeBase200 },
  empty: {
    padding: tokens.spacingHorizontalXXL,
    textAlign: 'center',
    color: tokens.colorNeutralForeground3,
    border: `1px dashed ${tokens.colorNeutralStroke2}`,
    borderRadius: tokens.borderRadiusMedium,
  },
  errorCard: { padding: tokens.spacingHorizontalL, color: tokens.colorPaletteRedForeground1 },
  loadingCard: { padding: tokens.spacingHorizontalXXL, display: 'flex', justifyContent: 'center' },
  dialogForm: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalL },
  remoteCell: { display: 'flex', alignItems: 'center', gap: tokens.spacingHorizontalXS },
  arrowIcon: { color: tokens.colorNeutralForeground3 },
  peerLink: {
    color: tokens.colorBrandForeground1,
    fontWeight: tokens.fontWeightSemibold,
    textDecoration: 'none',
  },
  inboundChip: {
    fontSize: tokens.fontSizeBase100,
    color: tokens.colorNeutralForeground3,
    fontStyle: 'italic',
  },
});

interface Peering {
  id: string;
  vnet_id: string;
  peer_vnet_id: string;
  tenant_id: string;
  name: string;
  allow_forwarded_traffic: boolean;
  status: string;
  message?: string;
  created_at: string;
}

interface VNet {
  id: string;
  name: string;
  tenant_id: string;
  status: string;
  address_space: string[];
}

function fmtDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

interface PeeringsTabProps {
  vnetId: string;
  tenantId: string;
  projectId: string;
}

export default function PeeringsTab({ vnetId, tenantId, projectId }: PeeringsTabProps) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);
  const [peerVnetId, setPeerVnetId] = useState('');
  const [name, setName] = useState('');
  const [allowForwarded, setAllowForwarded] = useState(false);

  const peeringsQuery = useQuery({
    queryKey: ['peerings', tenantId, projectId, vnetId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings', {
        params: { path: { tenant_id: tenantId, project_id: projectId, vnet_id: vnetId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Peering[];
    },
    refetchInterval: (q) => {
      const data = q.state.data as Peering[] | undefined;
      const transitioning = data?.some((p) => p.status === 'PENDING' || p.status === 'DELETING');
      return transitioning ? 5_000 : false;
    },
  });

  // For peer-VNet name lookup + the create dropdown options.
  const vnetsQuery = useQuery({
    queryKey: ['vnets', tenantId, projectId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets', {
        params: { path: { tenant_id: tenantId, project_id: projectId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as VNet[];
    },
  });

  const allVnets = vnetsQuery.data ?? [];
  const vnetById = new Map(allVnets.map((v) => [v.id, v]));

  // Candidates for "peer with" — every VNet in this tenant that is ACTIVE,
  // is not this VNet itself, and isn't already peered with this VNet.
  const peerings = peeringsQuery.data ?? [];
  const alreadyPeeredIds = new Set(
    peerings.flatMap((p) => [p.vnet_id, p.peer_vnet_id]).filter((id) => id !== vnetId)
  );
  const candidatePeers = allVnets.filter(
    (v) => v.id !== vnetId && v.status === 'ACTIVE' && !alreadyPeeredIds.has(v.id)
  );

  const nameValid = /^[a-z][a-z0-9-]{0,61}[a-z0-9]$/.test(name);
  const formValid = nameValid && Boolean(peerVnetId);

  const createMutation = useMutation({
    mutationFn: async () => {
      const body: Record<string, unknown> = {
        name,
        peer_vnet_id: peerVnetId,
        allow_forwarded_traffic: allowForwarded,
      };
      const { error } = await api.POST('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings', {
        params: { path: { tenant_id: tenantId, project_id: projectId, vnet_id: vnetId } },
        body: body as never,
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Peering creating</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['peerings', tenantId, projectId, vnetId] });
      setCreateOpen(false);
      setPeerVnetId('');
      setName('');
      setAllowForwarded(false);
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Create failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (peering: Peering) => {
      // The peering is owned by whichever VNet initiated it. We must call
      // DELETE on that side; the API rejects DELETE from the peer side
      // (Azure pattern). Find the owning VNet.
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/peerings/{peering_id}', {
        params: { path: { tenant_id: tenantId, project_id: projectId, vnet_id: peering.vnet_id, peering_id: peering.id } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Delete requested</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['peerings', tenantId, projectId, vnetId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDelete = async (p: Peering) => {
    const isLocal = p.vnet_id === vnetId;
    const ok = await confirmDialog({
      title: `Delete peering "${p.name}"?`,
      body: isLocal
        ? 'Cross-VNet traffic between the two networks will stop immediately. This cannot be undone.'
        : 'This peering was initiated from the other VNet. Deleting it here will remove it for both sides.',
      confirmLabel: 'Delete peering',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate(p);
  };

  // Determine the "remote" VNet for each row, relative to the VNet we're viewing.
  const rows = peerings.map((p) => {
    const remoteId = p.vnet_id === vnetId ? p.peer_vnet_id : p.vnet_id;
    const remote = vnetById.get(remoteId);
    const isInbound = p.vnet_id !== vnetId;
    return { peering: p, remoteId, remoteName: remote?.name ?? remoteId, isInbound };
  });

  const selectedPeer = candidatePeers.find((v) => v.id === peerVnetId);

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.toolbar}>
        <Button
          appearance="primary"
          icon={<Add20Regular />}
          onClick={() => setCreateOpen(true)}
          disabled={candidatePeers.length === 0}
        >
          Create peering
        </Button>
      </div>

      {peeringsQuery.isLoading && (
        <div className={styles.loadingCard}>
          <Spinner label="Loading peerings…" />
        </div>
      )}

      {peeringsQuery.isError && !peeringsQuery.isLoading && (
        <div className={styles.errorCard}>
          Failed to load peerings: {(peeringsQuery.error as Error).message}
        </div>
      )}

      {!peeringsQuery.isLoading && !peeringsQuery.isError && peerings.length === 0 && (
        <div className={styles.empty}>
          <Body1>
            No peerings yet. A peering links this VNet to another VNet in the same tenant —
            traffic flows in both directions once active.
          </Body1>
        </div>
      )}

      {peerings.length > 0 && (
        <Table size="small" aria-label="Peerings">
          <TableHeader>
            <TableRow>
              <TableHeaderCell>Name</TableHeaderCell>
              <TableHeaderCell>Status</TableHeaderCell>
              <TableHeaderCell>Peered VNet</TableHeaderCell>
              <TableHeaderCell>Created</TableHeaderCell>
              <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map(({ peering: p, remoteId, remoteName, isInbound }) => (
              <TableRow key={p.id}>
                <TableCell>
                  <Body1 style={{ fontWeight: 600 }}>{p.name}</Body1>
                  <div className={styles.mutedCell}>{p.id}</div>
                </TableCell>
                <TableCell>
                  <StatusPill status={p.status} />
                </TableCell>
                <TableCell>
                  <div className={styles.remoteCell}>
                    <ArrowsBidirectional20Regular className={styles.arrowIcon} />
                    <Link
                      to={`/tenants/${tenantId}/projects/${projectId}/vnets/${remoteId}`}
                      className={styles.peerLink}
                    >
                      {remoteName}
                    </Link>
                    {isInbound && (
                      <span className={styles.inboundChip}>(created from peer)</span>
                    )}
                  </div>
                </TableCell>
                <TableCell className={styles.mutedCell}>{fmtDate(p.created_at)}</TableCell>
                <TableCell>
                  <Menu>
                    <MenuTrigger disableButtonEnhancement>
                      <Button
                        appearance="subtle"
                        icon={<MoreHorizontal20Regular />}
                        aria-label="Actions"
                      />
                    </MenuTrigger>
                    <MenuPopover>
                      <MenuList>
                        <MenuItem
                          onClick={() => onDelete(p)}
                          disabled={deleteMutation.isPending}
                        >
                          Delete
                        </MenuItem>
                      </MenuList>
                    </MenuPopover>
                  </Menu>
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      <Dialog open={createOpen} onOpenChange={(_, d) => setCreateOpen(d.open)}>
        <DialogSurface>
          <DialogBody>
            <DialogTitle>Create peering</DialogTitle>
            <DialogContent>
              <div className={styles.dialogForm}>
                <Field
                  label="Name"
                  required
                  hint="Lowercase letters, numbers, hyphens. Unique within this VNet."
                  validationState={name && !nameValid ? 'error' : 'none'}
                  validationMessage={
                    name && !nameValid
                      ? 'Must start with a letter; only lowercase letters, numbers, hyphens.'
                      : undefined
                  }
                >
                  <Input
                    value={name}
                    onChange={(_, d) => setName(d.value.toLowerCase().replace(/[^a-z0-9-]/g, ''))}
                    placeholder="e.g. prod-to-dev"
                  />
                </Field>
                <Field
                  label="Peer with"
                  required
                  hint="Select another VNet in this tenant. Must be Active."
                >
                  <Dropdown
                    placeholder={candidatePeers.length === 0 ? 'No eligible VNets' : 'Select a VNet'}
                    value={selectedPeer?.name ?? ''}
                    selectedOptions={peerVnetId ? [peerVnetId] : []}
                    onOptionSelect={(_, d) => setPeerVnetId(d.optionValue ?? '')}
                  >
                    {candidatePeers.map((v) => (
                      <Option key={v.id} value={v.id} text={v.name}>
                        {v.name}
                      </Option>
                    ))}
                  </Dropdown>
                </Field>
                <Switch
                  label="Allow forwarded traffic (accepted but not yet enforced)"
                  checked={allowForwarded}
                  onChange={(_, d) => setAllowForwarded(d.checked)}
                />
              </div>
            </DialogContent>
            <DialogActions>
              <DialogTrigger disableButtonEnhancement>
                <Button appearance="subtle" disabled={createMutation.isPending}>Cancel</Button>
              </DialogTrigger>
              <Button
                appearance="primary"
                onClick={() => createMutation.mutate()}
                disabled={!formValid || createMutation.isPending}
              >
                {createMutation.isPending ? 'Creating…' : 'Create'}
              </Button>
            </DialogActions>
          </DialogBody>
        </DialogSurface>
      </Dialog>
    </div>
  );
}
