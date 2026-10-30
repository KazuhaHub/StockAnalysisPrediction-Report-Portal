/* Compare the native kernel UAPI layout with the bounded Go adapter. */
#include <stddef.h>
#include <stdio.h>
#include <sys/ioctl.h>
#include <sys/quota.h>
#include <linux/fs.h>
#include <linux/dqblk_xfs.h>
void print_geometry_fields(void);
int main(void) {
 printf("{\"status_size\":%zu,\"quota_size\":%zu,\"attr_size\":%zu,\"status_flags\":%zu,\"block_hard\":%zu,\"inode_hard\":%zu,\"used_blocks\":%zu,\"used_inodes\":%zu,\"project\":%zu,\"get_attr\":%lu,\"set_attr\":%lu,\"status_cmd\":%u,\"get_cmd\":%u,\"set_cmd\":%u",
 sizeof(struct fs_quota_statv),sizeof(fs_disk_quota_t),sizeof(struct fsxattr),
 offsetof(struct fs_quota_statv,qs_flags),offsetof(fs_disk_quota_t,d_blk_hardlimit),offsetof(fs_disk_quota_t,d_ino_hardlimit),offsetof(fs_disk_quota_t,d_bcount),offsetof(fs_disk_quota_t,d_icount),offsetof(struct fsxattr,fsx_projid),
 (unsigned long)FS_IOC_FSGETXATTR,(unsigned long)FS_IOC_FSSETXATTR,
 (unsigned)QCMD(Q_XGETQSTATV,PRJQUOTA),(unsigned)QCMD(Q_XGETQUOTA,PRJQUOTA),(unsigned)QCMD(Q_XSETQLIM,PRJQUOTA));
 print_geometry_fields();
 puts("}");
 return 0;
}
