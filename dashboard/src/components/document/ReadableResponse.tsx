import {Markdown} from '../Markdown';
import {readableResponse,type ReadableValue} from './responseModel';
export function ResponseValue({value}: {value:ReadableValue}) {
 switch(value.kind){
  case 'markdown': return <Markdown text={value.text}/>;
  case 'json':return <pre>{JSON.stringify(value.value,null,2)}</pre>;
  case 'fields':return <div className="readable-response-fields">{value.fields.map(field=><section key={field.name}><h3>{field.name}</h3><ResponseValue value={field.value}/></section>)}</div>;
 }
}
export function ReadableResponse({text}: {text:string}) {return <ResponseValue value={readableResponse(text)}/>;}
