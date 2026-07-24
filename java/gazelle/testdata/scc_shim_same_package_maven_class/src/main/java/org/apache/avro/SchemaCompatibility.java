package org.apache.avro;

import org.apache.avro.Schema.Field;
import org.apache.avro.Schema.Type;

public class SchemaCompatibility {
  public static boolean schemaNameEquals(final Schema reader, final Schema writer) {
    return reader.getFullName().equals(writer.getFullName());
  }

  public static Field lookupWriterField(final Schema writerSchema, final Field readerField) {
    if (writerSchema.getType() == Type.RECORD) {
      return readerField;
    }
    return null;
  }
}
