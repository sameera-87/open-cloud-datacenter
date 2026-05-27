import {
  Body1,
  Button,
  Menu,
  MenuItem,
  MenuList,
  MenuPopover,
  MenuTrigger,
  Spinner,
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
import { Add20Regular, MoreHorizontal20Regular } from '@fluentui/react-icons';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { useState } from 'react';
import { useParams } from 'react-router-dom';
import { useApi } from '../api/useApi';
import { useActiveProject } from '../hooks/useActiveProject';
import { useConfirmDialog } from './useConfirmDialog';
import StatusPill from './StatusPill';
import SubnetCreateDrawer from './SubnetCreateDrawer';

const useStyles = makeStyles({
  root: { display: 'flex', flexDirection: 'column', gap: tokens.spacingVerticalM },
  toolbar: { display: 'flex', gap: tokens.spacingHorizontalS },
  tableWrap: { padding: 0, overflow: 'hidden' },
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
});

interface Subnet {
  id: string;
  vnet_id: string;
  name: string;
  cidr: string;
  gateway?: string;
  description?: string;
  status: string;
  message?: string;
  created_at: string;
}

function fmtDate(iso: string): string {
  const d = new Date(iso);
  return isNaN(d.getTime()) ? iso : d.toLocaleString();
}

interface SubnetsTabProps {
  vnetId: string;
  /** Parent VNet's address_space — used to check the new subnet fits inside one of these. */
  vnetAddressSpace: string[];
}

export default function SubnetsTab({ vnetId, vnetAddressSpace }: SubnetsTabProps) {
  const styles = useStyles();
  const api = useApi();
  const queryClient = useQueryClient();
  const toasterId = useId('toaster');
  const { dispatchToast } = useToastController(toasterId);
  const { tenantId } = useParams<{ tenantId: string }>();
  const { projectId } = useActiveProject();
  const confirmDialog = useConfirmDialog();

  const [createOpen, setCreateOpen] = useState(false);

  const subnetsQuery = useQuery({
    queryKey: ['subnets', tenantId, projectId, vnetId],
    enabled: Boolean(tenantId) && Boolean(projectId),
    queryFn: async () => {
      const { data, error } = await api.GET('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId } },
      });
      if (error) throw new Error(typeof error === 'string' ? error : JSON.stringify(error));
      return (data ?? []) as Subnet[];
    },
    refetchInterval: (q) => {
      const data = q.state.data as Subnet[] | undefined;
      const transitioning = data?.some((s) => s.status === 'PENDING' || s.status === 'DELETING');
      return transitioning ? 5_000 : false;
    },
  });

  const subnets = subnetsQuery.data ?? [];

  const deleteMutation = useMutation({
    mutationFn: async (subnetId: string) => {
      const { error } = await api.DELETE('/v1/tenants/{tenant_id}/projects/{project_id}/vnets/{vnet_id}/subnets/{subnet_id}', {
        params: { path: { tenant_id: tenantId!, project_id: projectId!, vnet_id: vnetId, subnet_id: subnetId } },
      });
      if (error) {
        // dc-api error shape is { error: string }; surface the human message
        // so the 409 "attached resources" toast reads cleanly instead of as JSON.
        const msg =
          typeof error === 'string'
            ? error
            : typeof (error as { error?: unknown }).error === 'string'
              ? (error as { error: string }).error
              : JSON.stringify(error);
        throw new Error(msg);
      }
    },
    onSuccess: () => {
      dispatchToast(<Toast><ToastTitle>Delete requested</ToastTitle></Toast>, { intent: 'success' });
      queryClient.invalidateQueries({ queryKey: ['subnets', tenantId, projectId, vnetId] });
    },
    onError: (e: Error) => {
      dispatchToast(<Toast><ToastTitle>Delete failed: {e.message}</ToastTitle></Toast>, { intent: 'error' });
    },
  });

  const onDelete = async (s: Subnet) => {
    const ok = await confirmDialog({
      title: `Delete subnet "${s.name}"?`,
      body: `CIDR ${s.cidr} will be released. VMs and bastions attached to this subnet will lose connectivity. This cannot be undone.`,
      confirmLabel: 'Delete',
      destructive: true,
    });
    if (!ok) return;
    deleteMutation.mutate(s.id);
  };

  return (
    <div className={styles.root}>
      <Toaster toasterId={toasterId} />

      <div className={styles.toolbar}>
        <Button appearance="primary" icon={<Add20Regular />} onClick={() => setCreateOpen(true)}>
          Create subnet
        </Button>
      </div>

      {subnetsQuery.isLoading && (
        <div className={styles.loadingCard}>
          <Spinner label="Loading subnets…" />
        </div>
      )}

      {subnetsQuery.isError && !subnetsQuery.isLoading && (
        <div className={styles.errorCard}>
          Failed to load subnets: {(subnetsQuery.error as Error).message}
        </div>
      )}

      {!subnetsQuery.isLoading && !subnetsQuery.isError && subnets.length === 0 && (
        <div className={styles.empty}>
          <Body1>No subnets yet. Create one above to start placing VMs into this VNet.</Body1>
        </div>
      )}

      {subnets.length > 0 && (
        <div className={styles.tableWrap}>
          <Table size="small" aria-label="Subnets">
            <TableHeader>
              <TableRow>
                <TableHeaderCell>Name</TableHeaderCell>
                <TableHeaderCell>Status</TableHeaderCell>
                <TableHeaderCell>CIDR</TableHeaderCell>
                <TableHeaderCell>Gateway</TableHeaderCell>
                <TableHeaderCell>Created</TableHeaderCell>
                <TableHeaderCell style={{ width: 40 }}></TableHeaderCell>
              </TableRow>
            </TableHeader>
            <TableBody>
              {subnets.map((s) => (
                <TableRow key={s.id}>
                  <TableCell>
                    <Body1 style={{ fontWeight: 600 }}>{s.name}</Body1>
                    <div className={styles.mutedCell}>{s.id}</div>
                  </TableCell>
                  <TableCell>
                    <StatusPill status={s.status} />
                  </TableCell>
                  <TableCell className={styles.monoCell}>{s.cidr}</TableCell>
                  <TableCell className={styles.monoCell}>{s.gateway ?? '—'}</TableCell>
                  <TableCell className={styles.mutedCell}>{fmtDate(s.created_at)}</TableCell>
                  <TableCell>
                    <Menu>
                      <MenuTrigger disableButtonEnhancement>
                        <Button appearance="subtle" icon={<MoreHorizontal20Regular />} aria-label="Actions" />
                      </MenuTrigger>
                      <MenuPopover>
                        <MenuList>
                          <MenuItem onClick={() => onDelete(s)} disabled={deleteMutation.isPending}>
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
        </div>
      )}

      <SubnetCreateDrawer
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={() => {
          dispatchToast(<Toast><ToastTitle>Subnet creating</ToastTitle></Toast>, { intent: 'success' });
          setCreateOpen(false);
        }}
        vnetId={vnetId}
        vnetAddressSpace={vnetAddressSpace}
      />
    </div>
  );
}
