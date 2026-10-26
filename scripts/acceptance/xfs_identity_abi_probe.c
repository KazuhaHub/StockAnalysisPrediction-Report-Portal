/* Separate translation unit avoids duplicate Linux/XFS fsxattr declarations. */
#include <stddef.h>
#include <stdio.h>
#include <xfs/xfs.h>
void print_geometry_fields(void) {
 printf(",\"geometry_size\":%zu,\"geometry_uuid\":%zu,\"geometry_cmd\":%lu", sizeof(struct xfs_fsop_geom_v1), offsetof(struct xfs_fsop_geom_v1,uuid), (unsigned long)XFS_IOC_FSGEOMETRY_V1);
}
